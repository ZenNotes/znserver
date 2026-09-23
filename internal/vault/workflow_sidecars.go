package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A note's comments live in .zennotes/comments, filed under the note's path,
// so a workflow move that carried only the Markdown left them behind under the
// old name, detached from the note, where the next note given that name took
// them over. The prepared run now lists its moves, and this file carries the
// comments for each one and records them for undo.
//
// SYNCED FORMAT: the desktop applier (apps/desktop/src/main/workflow-apply.ts
// in ZenNotes/zennotes) writes the same `sidecars` array into its ledgers, so
// a vault opened by both can undo either's runs, and its rules are followed
// here: a leftover comments file at a destination refuses the run, entries
// name the NOTE and never the sidecar's own file, that file is derived at
// restore time after the note path has passed resolveWorkflowNotePath (so a
// ledger read off disk can only ever aim a restore at a note's own comments or
// creation date, never at a workflow or another run's record), and `after` is
// the hash of what the run left there, left out where the run cannot know.
// The creation-date file is the desktop's; it is never written here, only put
// back from a ledger the desktop wrote.

const (
	sidecarComments    = "comments"
	sidecarMetadata    = "metadata"
	noteMetadataRelDir = ".zennotes/note-metadata"
	noteMetadataSuffix = ".metadata.json"
)

type workflowSidecarEntry struct {
	Note    string
	Sidecar string
	Before  *string
	// After is the hash of what the run left at the sidecar's path, nil for
	// removed. HasAfter is false while the run has not got that far.
	After    *string
	HasAfter bool
}

func (e workflowSidecarEntry) MarshalJSON() ([]byte, error) {
	wire := struct {
		Note    string          `json:"note"`
		Sidecar string          `json:"sidecar"`
		Before  *string         `json:"before"`
		After   json.RawMessage `json:"after,omitempty"`
	}{Note: e.Note, Sidecar: e.Sidecar, Before: e.Before}
	if e.HasAfter {
		after, err := json.Marshal(e.After)
		if err != nil {
			return nil, err
		}
		wire.After = after
	}
	return json.Marshal(wire)
}

func (e *workflowSidecarEntry) UnmarshalJSON(data []byte) error {
	var wire struct {
		Note    string          `json:"note"`
		Sidecar string          `json:"sidecar"`
		Before  *string         `json:"before"`
		After   json.RawMessage `json:"after"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*e = workflowSidecarEntry{Note: wire.Note, Sidecar: wire.Sidecar, Before: wire.Before}
	if len(wire.After) > 0 {
		var after *string
		if err := json.Unmarshal(wire.After, &after); err != nil {
			return err
		}
		e.After = after
		e.HasAfter = true
	}
	return nil
}

// workflowSidecarPath derives a sidecar's file from its note's path, which is
// checked exactly as a journal entry's is first.
func (v *Vault) workflowSidecarPath(note, sidecar string) (string, error) {
	if _, err := v.resolveWorkflowNotePath(note); err != nil {
		return "", err
	}
	rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(note)))
	switch sidecar {
	case sidecarComments:
		return v.commentsPath(rel)
	case sidecarMetadata:
		return SafeJoin(v.root, noteMetadataRelDir+"/"+rel+noteMetadataSuffix)
	}
	return "", fmt.Errorf("%w: unknown sidecar %q", ErrInvalidWorkflow, sidecar)
}

func (v *Vault) sidecarRoot(sidecar string) string {
	if sidecar == sidecarMetadata {
		return filepath.Join(v.root, filepath.FromSlash(noteMetadataRelDir))
	}
	return v.commentsRoot()
}

// leftoverCommentsMessage is the desktop's wording, so a refusal reads the
// same whichever side moved the note.
func (v *Vault) leftoverCommentsMessage(note, commentsAbs string) string {
	name := strings.TrimSuffix(filepath.Base(note), filepath.Ext(note))
	rel, err := filepath.Rel(v.root, commentsAbs)
	if err != nil {
		rel = commentsAbs
	}
	return fmt.Sprintf("Comments from an earlier note named “%s” are still in %s. Move or delete that file to use this name.", name, filepath.ToSlash(rel))
}

// validateWorkflowMoves checks the moves a prepared run lists against the
// changes it carries: each end must be a note path the run also names, so a
// move can never relocate the comments of a note the run leaves alone.
func (v *Vault) validateWorkflowMoves(moves []WorkflowRunMove, changed map[string]struct{}) ([]WorkflowRunMove, error) {
	if len(moves) > maxWorkflowChanges {
		return nil, fmt.Errorf("%w: this run lists %d moves, over the server limit of %d", ErrInvalidWorkflow, len(moves), maxWorkflowChanges)
	}
	normalized := make([]WorkflowRunMove, 0, len(moves))
	for _, move := range moves {
		from := filepath.ToSlash(filepath.Clean(filepath.FromSlash(move.From)))
		to := filepath.ToSlash(filepath.Clean(filepath.FromSlash(move.To)))
		for _, end := range []string{from, to} {
			if _, err := v.resolveWorkflowNotePath(end); err != nil {
				return nil, err
			}
			if _, ok := changed[workflowJournalKey(end)]; !ok {
				return nil, fmt.Errorf("%w: workflow move names %s, which the run does not change", ErrInvalidWorkflow, end)
			}
		}
		if from == to {
			continue
		}
		normalized = append(normalized, WorkflowRunMove{From: from, To: to})
	}
	return normalized, nil
}

type plannedSidecarMove struct {
	from, to string
}

// planWorkflowSidecarMoves reads, before anything is written, what each move
// will carry: the comments at its source (through the moves before it, so a
// note moved twice carries them on from the middle path) and whatever waits
// at its destination. Leftover comments there refuse the whole run, since
// taking over another note's discussion and deleting it are both wrong, and
// the message names the file to move aside. First touch wins in the entries,
// as it does for notes.
func (v *Vault) planWorkflowSidecarMoves(moves []WorkflowRunMove) (entries []workflowSidecarEntry, renames []plannedSidecarMove, refusal string, err error) {
	live := map[string]*string{}
	read := func(abs string) (*string, error) {
		if value, ok := live[abs]; ok {
			return value, nil
		}
		value, err := readOptionalText(abs)
		if err != nil {
			return nil, err
		}
		live[abs] = value
		return value, nil
	}
	seen := map[string]struct{}{}
	touch := func(note string, before *string) {
		key := workflowJournalKey(note)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		entries = append(entries, workflowSidecarEntry{Note: note, Sidecar: sidecarComments, Before: before})
	}
	for _, move := range moves {
		fromAbs, err := v.workflowSidecarPath(move.From, sidecarComments)
		if err != nil {
			return nil, nil, "", err
		}
		toAbs, err := v.workflowSidecarPath(move.To, sidecarComments)
		if err != nil {
			return nil, nil, "", err
		}
		toBytes, err := read(toAbs)
		if err != nil {
			return nil, nil, "", err
		}
		if toBytes != nil {
			return nil, nil, v.leftoverCommentsMessage(move.To, toAbs), nil
		}
		fromBytes, err := read(fromAbs)
		if err != nil {
			return nil, nil, "", err
		}
		if fromBytes == nil {
			continue
		}
		touch(move.From, fromBytes)
		touch(move.To, nil)
		live[fromAbs] = nil
		live[toAbs] = fromBytes
		renames = append(renames, plannedSidecarMove{from: fromAbs, to: toAbs})
	}
	return entries, renames, "", nil
}

// applyWorkflowSidecarMoves carries the comments, in the order the notes moved.
func (v *Vault) applyWorkflowSidecarMoves(renames []plannedSidecarMove) error {
	for _, rename := range renames {
		if err := os.MkdirAll(filepath.Dir(rename.to), v.dirMode); err != nil {
			return err
		}
		if err := os.Rename(rename.from, rename.to); err != nil {
			return err
		}
		v.pruneEmptySidecarDirs(v.commentsRoot(), filepath.Dir(rename.from))
	}
	return nil
}

// finishedSidecars fills in what the run left at each sidecar's path once
// every move has landed: the last move away leaves nothing, the last move in
// leaves the bytes it carried.
func (v *Vault) finishedSidecars(entries []workflowSidecarEntry) []workflowSidecarEntry {
	finished := make([]workflowSidecarEntry, 0, len(entries))
	for _, entry := range entries {
		abs, err := v.workflowSidecarPath(entry.Note, entry.Sidecar)
		if err != nil {
			finished = append(finished, entry)
			continue
		}
		live, err := readOptionalText(abs)
		if err != nil {
			finished = append(finished, entry)
			continue
		}
		entry.After = workflowHash(live)
		entry.HasAfter = true
		finished = append(finished, entry)
	}
	return finished
}

// restoreWorkflowSidecarsLocked puts every recorded sidecar back, the way the
// journal restore does notes; a failure names the note, which is the name the
// user knows.
func (v *Vault) restoreWorkflowSidecarsLocked(entries []workflowSidecarEntry) []error {
	failures := []error{}
	for _, entry := range entries {
		abs, err := v.workflowSidecarPath(entry.Note, entry.Sidecar)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s (its %s): %w", entry.Note, entry.Sidecar, err))
			continue
		}
		live, err := readOptionalText(abs)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s (its %s): %w", entry.Note, entry.Sidecar, err))
			continue
		}
		if optionalStringsEqual(live, entry.Before) {
			continue
		}
		if entry.Before == nil {
			err = os.Remove(abs)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
			if err == nil {
				v.pruneEmptySidecarDirs(v.sidecarRoot(entry.Sidecar), filepath.Dir(abs))
			}
		} else {
			err = writeFileAtomic(abs, []byte(*entry.Before), v.fileMode, v.dirMode)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s (its %s): %w", entry.Note, entry.Sidecar, err))
		}
	}
	return failures
}

// restoreWorkflowRunLocked puts a run's notes and their sidecars back.
func (v *Vault) restoreWorkflowRunLocked(journal []workflowJournalEntry, sidecars []workflowSidecarEntry) []error {
	_, failures := v.restoreWorkflowJournalLocked(journal)
	return append(failures, v.restoreWorkflowSidecarsLocked(sidecars)...)
}

// pruneEmptySidecarDirs removes the directories a move or an undo emptied,
// up to (not including) the sidecar tree's root. Not clutter: a folder rename
// refuses a destination whose comments directory already exists (see
// relocateFolderTrees), so an empty one left behind would block that folder
// name for good with nothing visible to explain why.
func (v *Vault) pruneEmptySidecarDirs(base, dir string) {
	base = filepath.Clean(base)
	for strings.HasPrefix(dir, base+string(filepath.Separator)) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// driftedSidecarNotes names the notes whose sidecars no longer hold what the
// run left, so the user hears which comments an undo takes with it.
func (v *Vault) driftedSidecarNotes(entries []workflowSidecarEntry, already []string) []string {
	named := map[string]struct{}{}
	for _, path := range already {
		named[path] = struct{}{}
	}
	drifted := []string{}
	for _, entry := range entries {
		if !entry.HasAfter {
			continue
		}
		if _, ok := named[entry.Note]; ok {
			continue
		}
		abs, err := v.workflowSidecarPath(entry.Note, entry.Sidecar)
		if err != nil {
			continue
		}
		live, err := readOptionalText(abs)
		if err != nil {
			continue
		}
		if !optionalStringsEqual(workflowHash(live), entry.After) {
			named[entry.Note] = struct{}{}
			drifted = append(drifted, entry.Note)
		}
	}
	return drifted
}
