package vault

import (
	_ "embed"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed demo-tour.json
var demoTourJSON []byte

type demoFile struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

type demoTour struct {
	Notes  []demoFile `json:"notes"`
	Assets []demoFile `json:"assets"`
}

// DemoTourResult mirrors shared/ipc.ts VaultDemoTourResult.
type DemoTourResult struct {
	NotePaths  []string `json:"notePaths"`
	AssetPaths []string `json:"assetPaths"`
}

// olderDemoTourNote matches a tour note under the name the tour used until
// 2.68.0, "NN <long dash> Title.md" (U+2014); it is now "NN Title.md". Vaults
// seeded back then still hold those files. The match is exact on that
// separator because regenerating and removing the tour delete what it matches.
// A synced copy of currentDemoTourPath in the desktop's
// packages/shared-domain/src/demo-tour.ts.
var olderDemoTourNote = regexp.MustCompile(`^(\d{2}b?) \x{2014} (.+\.md)$`)

// olderDemoTourNotePaths lists the tour's notes still on disk under their
// older names, as vault-relative slash paths.
func (v *Vault) olderDemoTourNotePaths(tour *demoTour) []string {
	current := map[string]bool{}
	dirs := map[string]bool{}
	for _, note := range tour.Notes {
		current[note.Path] = true
		dirs[path.Dir(note.Path)] = true
	}
	older := []string{}
	for dir := range dirs {
		abs, err := SafeJoin(v.root, dir)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			m := olderDemoTourNote.FindStringSubmatch(entry.Name())
			if m != nil && current[dir+"/"+m[1]+" "+m[2]] {
				older = append(older, dir+"/"+entry.Name())
			}
		}
	}
	sort.Strings(older)
	return older
}

func loadDemoTour() (*demoTour, error) {
	tour := &demoTour{}
	if err := json.Unmarshal(demoTourJSON, tour); err != nil {
		return nil, err
	}
	return tour, nil
}

// GenerateDemoTour seeds the vault with the built-in tour notes and
// the demo attachment. Existing files are overwritten, and a tour seeded
// under the older note names is replaced rather than left beside the new one.
func (v *Vault) GenerateDemoTour() (DemoTourResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	tour, err := loadDemoTour()
	if err != nil {
		return DemoTourResult{}, err
	}
	for _, rel := range v.olderDemoTourNotePaths(tour) {
		if abs, err := SafeJoin(v.root, rel); err == nil {
			_ = os.Remove(abs)
		}
	}
	result := DemoTourResult{NotePaths: []string{}, AssetPaths: []string{}}
	for _, note := range tour.Notes {
		abs, err := SafeJoin(v.root, note.Path)
		if err != nil {
			return DemoTourResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(abs), v.dirMode); err != nil {
			return DemoTourResult{}, err
		}
		if err := os.WriteFile(abs, []byte(note.Body), v.fileMode); err != nil {
			return DemoTourResult{}, err
		}
		result.NotePaths = append(result.NotePaths, filepath.ToSlash(note.Path))
	}
	for _, asset := range tour.Assets {
		abs, err := SafeJoin(v.root, asset.Path)
		if err != nil {
			return DemoTourResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(abs), v.dirMode); err != nil {
			return DemoTourResult{}, err
		}
		if err := os.WriteFile(abs, []byte(asset.Body), v.fileMode); err != nil {
			return DemoTourResult{}, err
		}
		result.AssetPaths = append(result.AssetPaths, filepath.ToSlash(asset.Path))
	}
	return result, nil
}

// RemoveDemoTour deletes the demo notes + asset if they exist, including
// notes still under the older names. Also removes empty parent directories
// under inbox/demo.
func (v *Vault) RemoveDemoTour() (DemoTourResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	tour, err := loadDemoTour()
	if err != nil {
		return DemoTourResult{}, err
	}
	result := DemoTourResult{NotePaths: []string{}, AssetPaths: []string{}}
	removedDirs := map[string]bool{}
	notePaths := []string{}
	for _, note := range tour.Notes {
		notePaths = append(notePaths, note.Path)
	}
	notePaths = append(notePaths, v.olderDemoTourNotePaths(tour)...)
	for _, rel := range notePaths {
		abs, err := SafeJoin(v.root, rel)
		if err != nil {
			continue
		}
		if err := os.Remove(abs); err == nil {
			result.NotePaths = append(result.NotePaths, filepath.ToSlash(rel))
			removedDirs[filepath.Dir(abs)] = true
		}
	}
	for _, asset := range tour.Assets {
		abs, err := SafeJoin(v.root, asset.Path)
		if err != nil {
			continue
		}
		if err := os.Remove(abs); err == nil {
			result.AssetPaths = append(result.AssetPaths, filepath.ToSlash(asset.Path))
		}
	}
	for dir := range removedDirs {
		// Walk up from each removed-note directory and rmdir empties,
		// stopping at the vault root.
		d := dir
		for strings.HasPrefix(d, v.root) && d != v.root {
			if err := os.Remove(d); err != nil {
				break
			}
			d = filepath.Dir(d)
		}
	}
	return result, nil
}
