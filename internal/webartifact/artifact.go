// Package webartifact verifies pinned browser distributions for Go-only builds.
package webartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxManifest = 4 << 20
	maxArchive  = 256 << 20
	maxExpanded = 512 << 20
	maxFiles    = 4096
)

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Archive struct {
	File   string `json:"file"`
	URL    string `json:"url,omitempty"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Source struct {
	Repository     string `json:"repository"`
	Commit         string `json:"commit"`
	Dirty          *bool  `json:"dirty"`
	LockfileSHA256 string `json:"lockfileSha256,omitempty"`
}

type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	Artifact      string            `json:"artifact"`
	Version       string            `json:"version"`
	Protocol      string            `json:"protocol"`
	Source        Source            `json:"source"`
	Toolchain     map[string]string `json:"toolchain,omitempty"`
	Archive       Archive           `json:"archive"`
	Entrypoints   []string          `json:"entrypoints"`
	Files         []File            `json:"files"`
}

var checksum = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commit = regexp.MustCompile(`^[a-f0-9]{40}$`)

func portablePath(name string) bool {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, `\:*?"<>|`) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimRight(part, ". ") != part {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9') {
			return false
		}
		for _, char := range part {
			if char < 32 || char == 127 {
				return false
			}
		}
	}
	return true
}

// Reject ambiguous keys before decoding into structs, where JSON otherwise uses
// the last duplicate. Limit nesting independently of the manifest's byte limit.
func checkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("manifest nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[strings.ToLower(name)] {
				return fmt.Errorf("duplicate or invalid manifest key: %v", key)
			}
			seen[strings.ToLower(name)] = true
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	case json.Delim('['):
		for decoder.More() {
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	}
	return err
}

func ReadManifest(path string, allowDirty bool) (Manifest, error) {
	var manifest Manifest
	file, err := os.Open(path)
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxManifest+1))
	if err != nil {
		return manifest, err
	}
	if len(data) > maxManifest {
		return manifest, errors.New("artifact manifest is too large")
	}
	if err := checkJSONValue(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return manifest, errors.New("unexpected data after manifest")
	}
	if manifest.SchemaVersion != 1 || manifest.Artifact != "zennotes-self-hosted-web" || manifest.Protocol != "self-hosted-http-v1" {
		return manifest, errors.New("unsupported browser artifact or protocol")
	}
	if manifest.Version == "" || !commit.MatchString(manifest.Source.Commit) || manifest.Source.Repository != "https://github.com/ZenNotes/zennotes" {
		return manifest, errors.New("invalid artifact provenance")
	}
	if manifest.Source.Dirty == nil {
		return manifest, errors.New("source.dirty must be an explicit boolean")
	}
	if manifest.Source.LockfileSHA256 != "" && !checksum.MatchString(manifest.Source.LockfileSHA256) {
		return manifest, errors.New("invalid lockfile checksum")
	}
	if *manifest.Source.Dirty && !allowDirty {
		return manifest, errors.New("uncommitted source requires -allow-dirty for local testing")
	}
	if !portablePath(manifest.Archive.File) || strings.Contains(manifest.Archive.File, "/") ||
		!strings.HasSuffix(manifest.Archive.File, ".tgz") || !checksum.MatchString(manifest.Archive.SHA256) ||
		manifest.Archive.Size <= 0 || manifest.Archive.Size > maxArchive {
		return manifest, errors.New("invalid archive pin")
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > maxFiles {
		return manifest, errors.New("invalid artifact file count")
	}
	paths := make(map[string]bool)
	var total int64
	for _, item := range manifest.Files {
		key := strings.ToLower(item.Path)
		if !portablePath(item.Path) || paths[key] || !checksum.MatchString(item.SHA256) || item.Size < 0 || item.Size > 128<<20 {
			return manifest, fmt.Errorf("invalid or duplicate asset %q", item.Path)
		}
		paths[key] = true
		total += item.Size
	}
	if total > maxExpanded {
		return manifest, errors.New("expanded artifact is too large")
	}
	if len(manifest.Entrypoints) == 0 {
		return manifest, errors.New("missing artifact entrypoints")
	}
	hasIndex := false
	for _, name := range manifest.Entrypoints {
		found := false
		for _, item := range manifest.Files {
			if item.Path == name && item.Size > 0 {
				found = true
			}
		}
		if !found {
			return manifest, fmt.Errorf("entrypoint %q is missing from inventory", name)
		}
		if name == "index.html" {
			hasIndex = true
		}
	}
	if !hasIndex {
		return manifest, errors.New("missing index.html entrypoint")
	}
	return manifest, nil
}

func validateHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("artifact download requires an HTTPS URL without credentials or a fragment")
	}
	return nil
}

func openArchive(ctx context.Context, manifestPath, override string, archive Archive) (io.ReadCloser, error) {
	if override != "" {
		return os.Open(override)
	}
	local, err := os.Open(filepath.Join(filepath.Dir(manifestPath), archive.File))
	if err == nil {
		return local, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := validateHTTPS(archive.URL); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many artifact redirects")
		}
		return validateHTTPS(req.URL.String())
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archive.URL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("artifact download: HTTP %d", response.StatusCode)
	}
	return response.Body, nil
}

func verifyFile(path string, expected File) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, expected.Size+1))
	if err != nil {
		return err
	}
	if n != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("asset checksum or size mismatch: %s", expected.Path)
	}
	return nil
}

func verifyTree(root string, manifest Manifest) error {
	want := make(map[string]File, len(manifest.Files))
	for _, item := range manifest.Files {
		want[item.Path] = item
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("artifact tree contains a symbolic link")
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		item, ok := want[name]
		if !ok || !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected artifact file: %s", name)
		}
		if err := verifyFile(path, item); err != nil {
			return err
		}
		delete(want, name)
		return nil
	})
	if err != nil {
		return err
	}
	if len(want) != 0 {
		return errors.New("artifact tree is incomplete")
	}
	return nil
}

func extract(archive *os.File, stage string, manifest Manifest) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: maxExpanded + 16<<20}
	reader := tar.NewReader(limited)
	want := make(map[string]File, len(manifest.Files))
	for _, item := range manifest.Files {
		want[item.Path] = item
	}
	seen := make(map[string]bool)
	for count := 0; ; count++ {
		if count > maxFiles*2 {
			return errors.New("too many archive entries")
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !portablePath(name) || seen[strings.ToLower(name)] {
			return fmt.Errorf("unsafe or duplicate archive path %q", header.Name)
		}
		seen[strings.ToLower(name)] = true
		if header.Typeflag == tar.TypeDir {
			if name != "package" && name != "package/dist" && !strings.HasPrefix(name, "package/dist/") {
				return errors.New("unexpected archive directory")
			}
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("unsupported archive entry %q", name)
		}
		if name == "package/package.json" || name == "package/LICENSE" {
			if header.Size > 64<<10 {
				return errors.New("oversized package metadata")
			}
			continue
		}
		rel := strings.TrimPrefix(name, "package/dist/")
		item, ok := want[rel]
		if !strings.HasPrefix(name, "package/dist/") || !ok || header.Size != item.Size {
			return fmt.Errorf("unexpected asset or size: %q", name)
		}
		destination := filepath.Join(stage, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(file, hash), reader)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != item.Size || hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
			return fmt.Errorf("asset checksum mismatch: %s", rel)
		}
		delete(want, rel)
	}
	if len(want) != 0 {
		return errors.New("archive is missing declared assets")
	}
	// Read through gzip's checksum, accepting only tar's trailing zero padding.
	buffer := make([]byte, 32<<10)
	for {
		n, err := limited.Read(buffer)
		for _, b := range buffer[:n] {
			if b != 0 {
				return errors.New("unexpected data after tar archive")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if limited.N <= 0 {
		return errors.New("expanded archive exceeds limit")
	}
	return nil
}

// Install verifies an archive before exposing its files and serializes cooperating
// installers. Use a privately owned build directory: other processes must not
// mutate the destination or its parents during verification and publication.
// An existing distribution is reused only if all its files match the manifest.
func Install(ctx context.Context, manifestPath, archiveOverride, destination string, allowDirty bool) error {
	manifest, err := ReadManifest(manifestPath, allowDirty)
	if err != nil {
		return err
	}
	if destination == "" {
		return errors.New("an output directory is required")
	}
	destination = filepath.Clean(destination)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	lockPath := destination + ".install-lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("acquire artifact install lock: %w", err)
	}
	lock.Close()
	defer os.Remove(lockPath)
	if _, err := os.Lstat(destination); err == nil {
		if err := verifyTree(destination, manifest); err != nil {
			return fmt.Errorf("destination already exists; use a clean build directory: %w", err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	work, err := os.MkdirTemp(filepath.Dir(destination), ".web-artifact-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	input, err := openArchive(ctx, manifestPath, archiveOverride, manifest.Archive)
	if err != nil {
		return err
	}
	defer input.Close()
	archive, err := os.CreateTemp(work, "archive-")
	if err != nil {
		return err
	}
	defer archive.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(input, manifest.Archive.Size+1))
	if err != nil {
		return err
	}
	if n != manifest.Archive.Size || hex.EncodeToString(hash.Sum(nil)) != manifest.Archive.SHA256 {
		return errors.New("archive checksum or size mismatch")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	stage := filepath.Join(work, "dist")
	if err := os.Mkdir(stage, 0o755); err != nil {
		return err
	}
	if err := extract(archive, stage, manifest); err != nil {
		return err
	}
	return os.Rename(stage, destination)
}
