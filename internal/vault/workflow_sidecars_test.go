package vault

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A note's comments live in .zennotes/comments under the note's path. Before,
// a prepared run moved the Markdown alone and left them behind under the old
// name; now the run lists its moves and the server carries them, records
// them for undo, and refuses a destination where an earlier note's comments
// still sit, the way the desktop applier does.

const testComments = "{\n  \"version\": 1,\n  \"comments\": [\n    {\n      \"id\": \"c1\",\n      \"notePath\": \"inbox/A.md\",\n      \"anchorStart\": 0,\n      \"anchorEnd\": 0,\n      \"anchorText\": \"\",\n      \"body\": \"Keep this discussion\",\n      \"createdAt\": 1,\n      \"updatedAt\": 1,\n      \"resolvedAt\": null\n    }\n  ]\n}"

func commentsFile(root, note string) string {
	return filepath.Join(root, ".zennotes", "comments", filepath.FromSlash(note)+".comments.json")
}

func writeSidecar(t *testing.T, abs, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readOrMissing(t *testing.T, abs string) (string, bool) {
	t.Helper()
	body, err := os.ReadFile(abs)
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(body), true
}

func archiveRun(t *testing.T) PreparedWorkflowRun {
	t.Helper()
	body := "# A\n"
	return PreparedWorkflowRun{
		WorkflowID: "archive-a",
		Ops:        []json.RawMessage{rawWorkflowOp(t, map[string]any{"kind": "archive", "path": "inbox/A.md"})},
		Applied:    1,
		Changes: []WorkflowRunFileChange{
			{Path: "inbox/A.md", Before: &body, After: nil},
			{Path: "archive/A.md", Before: nil, After: &body},
		},
		Moves: []WorkflowRunMove{{From: "inbox/A.md", To: "archive/A.md"}},
	}
}

func TestPreparedWorkflowMoveCarriesComments(t *testing.T) {
	v, root := workflowTestVault(t)
	writeSidecar(t, commentsFile(root, "inbox/A.md"), testComments)

	receipt, err := v.ApplyPreparedWorkflow(archiveRun(t))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RolledBack != nil {
		t.Fatalf("run rolled back: %s", receipt.RolledBack.Reason)
	}
	if got, ok := readOrMissing(t, commentsFile(root, "archive/A.md")); !ok || got != testComments {
		t.Fatalf("comments did not travel with the note: present=%v", ok)
	}
	if _, ok := readOrMissing(t, commentsFile(root, "inbox/A.md")); ok {
		t.Fatal("comments were left behind at the old name")
	}
	moved, err := v.ReadNoteComments("archive/A.md")
	if err != nil || len(moved) != 1 || moved[0].Body != "Keep this discussion" || moved[0].NotePath != "archive/A.md" {
		t.Fatalf("the moved note does not read its discussion: %v %+v", err, moved)
	}
	ledger, err := v.readWorkflowLedgerLocked(receipt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Sidecars) != 2 || ledger.Sidecars[0].Note != "inbox/A.md" || ledger.Sidecars[0].Before == nil || ledger.Sidecars[1].Note != "archive/A.md" || ledger.Sidecars[1].Before != nil {
		t.Fatalf("ledger sidecars = %+v", ledger.Sidecars)
	}
	// The desktop's format, byte for byte: `after` null where the run moved the
	// file away, a hash where it left one.
	raw, err := os.ReadFile(filepath.Join(root, ".zennotes", "workflows", ".runs", receipt.RunID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"sidecar": "comments"`) || !strings.Contains(string(raw), `"after": null`) || !strings.Contains(string(raw), `"after": "`+*workflowHash(&[]string{testComments}[0])+`"`) {
		t.Fatalf("ledger does not carry the sidecars in the shared format:\n%s", raw)
	}
	// The journal itself names only notes, so an older reader still undoes them.
	if len(ledger.Journal) != 2 {
		t.Fatalf("journal = %+v", ledger.Journal)
	}

	undo, err := v.UndoWorkflowRun(receipt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if undo.Restored != 2 || len(undo.DriftedPaths) != 0 {
		t.Fatalf("undo = %+v", undo)
	}
	if got, ok := readOrMissing(t, commentsFile(root, "inbox/A.md")); !ok || got != testComments {
		t.Fatalf("comments did not come back: present=%v", ok)
	}
	if _, ok := readOrMissing(t, commentsFile(root, "archive/A.md")); ok {
		t.Fatal("comments were left at the destination after undo")
	}
	if _, err := os.Stat(filepath.Join(root, ".zennotes", "comments", "archive")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty comments folder left at the destination (a folder rename onto that name would refuse): %v", err)
	}
}

func TestPreparedWorkflowRefusesLeftoverComments(t *testing.T) {
	v, root := workflowTestVault(t)
	writeSidecar(t, commentsFile(root, "inbox/A.md"), testComments)
	writeSidecar(t, commentsFile(root, "archive/A.md"), "{\"version\":1,\"comments\":[]}")

	receipt, err := v.ApplyPreparedWorkflow(archiveRun(t))
	if err != nil {
		t.Fatal(err)
	}
	want := "Comments from an earlier note named “A” are still in .zennotes/comments/archive/A.md.comments.json. Move or delete that file to use this name. The run was rolled back; your vault is unchanged."
	if receipt.RolledBack == nil || receipt.RolledBack.Reason != want {
		t.Fatalf("receipt = %+v", receipt)
	}
	if _, ok := readOrMissing(t, filepath.Join(root, "inbox", "A.md")); !ok {
		t.Fatal("the note moved although the run was refused")
	}
	if got, ok := readOrMissing(t, commentsFile(root, "inbox/A.md")); !ok || got != testComments {
		t.Fatal("the note's comments were touched")
	}
	if runs, err := v.ListWorkflowRuns(); err != nil || len(runs) != 0 {
		t.Fatalf("a refused run left a record: %v %+v", err, runs)
	}
}

func TestPreparedWorkflowChainedMovesCarryComments(t *testing.T) {
	v, root := workflowTestVault(t)
	writeSidecar(t, commentsFile(root, "inbox/A.md"), testComments)
	body := "# A\n"
	receipt, err := v.ApplyPreparedWorkflow(PreparedWorkflowRun{
		WorkflowID: "chain",
		Ops: []json.RawMessage{
			rawWorkflowOp(t, map[string]any{"kind": "move", "path": "inbox/A.md", "to": "inbox/Work"}),
			rawWorkflowOp(t, map[string]any{"kind": "rename", "path": "inbox/Work/A.md", "to": "Final"}),
		},
		Applied: 2,
		Changes: []WorkflowRunFileChange{
			{Path: "inbox/A.md", Before: &body, After: nil},
			{Path: "inbox/Work/A.md", Before: nil, After: nil},
			{Path: "inbox/Work/Final.md", Before: nil, After: &body},
		},
		Moves: []WorkflowRunMove{
			{From: "inbox/A.md", To: "inbox/Work/A.md"},
			{From: "inbox/Work/A.md", To: "inbox/Work/Final.md"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RolledBack != nil {
		t.Fatalf("run rolled back: %s", receipt.RolledBack.Reason)
	}
	if got, ok := readOrMissing(t, commentsFile(root, "inbox/Work/Final.md")); !ok || got != testComments {
		t.Fatalf("comments did not follow the note through both moves: present=%v", ok)
	}
	if _, ok := readOrMissing(t, commentsFile(root, "inbox/Work/A.md")); ok {
		t.Fatal("comments were left at the middle path")
	}

	if _, err := v.UndoWorkflowRun(receipt.RunID); err != nil {
		t.Fatal(err)
	}
	if got, ok := readOrMissing(t, commentsFile(root, "inbox/A.md")); !ok || got != testComments {
		t.Fatalf("comments did not come back to the start: present=%v", ok)
	}
	for _, note := range []string{"inbox/Work/A.md", "inbox/Work/Final.md"} {
		if _, ok := readOrMissing(t, commentsFile(root, note)); ok {
			t.Fatalf("comments left at %s after undo", note)
		}
	}
}

func TestPreparedWorkflowRejectsMoveOutsideItsChanges(t *testing.T) {
	v, _ := workflowTestVault(t)
	run := archiveRun(t)
	run.Moves = []WorkflowRunMove{{From: "inbox/A.md", To: "inbox/B.md"}}

	_, err := v.ApplyPreparedWorkflow(run)
	if !errors.Is(err, ErrInvalidWorkflow) || !strings.Contains(err.Error(), "inbox/B.md") {
		t.Fatalf("err = %v", err)
	}
}

func TestPreparedWorkflowRollsBackWhenCommentsCannotMove(t *testing.T) {
	v, root := workflowTestVault(t)
	bodyA, bodyB := "# A\n", "# B\n"
	commentsB := strings.ReplaceAll(testComments, "inbox/A.md", "inbox/B.md")
	if err := os.WriteFile(filepath.Join(root, "inbox", "B.md"), []byte(bodyB), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSidecar(t, commentsFile(root, "inbox/A.md"), testComments)
	writeSidecar(t, commentsFile(root, "inbox/B.md"), commentsB)
	// A clash the run makes itself: A's comments land at
	// archive/A.md.comments.json, the path B's comments need as a folder,
	// since B moves into a note folder of that name. The plan reads the disk
	// before anything moves and cannot see it, so B's comments fail only after
	// both notes and A's comments have landed, and all of it has to come back.
	// Staged beforehand, the same file fails the plan's read on Linux and
	// macOS instead, and a read-only folder binds neither Windows nor root.
	clashing := "archive/A.md.comments.json/B.md"

	receipt, err := v.ApplyPreparedWorkflow(PreparedWorkflowRun{
		WorkflowID: "clash",
		Ops: []json.RawMessage{
			rawWorkflowOp(t, map[string]any{"kind": "archive", "path": "inbox/A.md"}),
			rawWorkflowOp(t, map[string]any{"kind": "move", "path": "inbox/B.md", "to": "archive/A.md.comments.json"}),
		},
		Applied: 2,
		Changes: []WorkflowRunFileChange{
			{Path: "inbox/A.md", Before: &bodyA, After: nil},
			{Path: "archive/A.md", Before: nil, After: &bodyA},
			{Path: "inbox/B.md", Before: &bodyB, After: nil},
			{Path: clashing, Before: nil, After: &bodyB},
		},
		Moves: []WorkflowRunMove{
			{From: "inbox/A.md", To: "archive/A.md"},
			{From: "inbox/B.md", To: clashing},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RolledBack == nil || !strings.Contains(receipt.RolledBack.Reason, "your vault is unchanged") {
		t.Fatalf("receipt = %+v", receipt)
	}
	for note, body := range map[string]string{"inbox/A.md": bodyA, "inbox/B.md": bodyB} {
		if got, ok := readOrMissing(t, filepath.Join(root, filepath.FromSlash(note))); !ok || got != body {
			t.Fatalf("%s was not put back", note)
		}
	}
	for _, note := range []string{"archive/A.md", clashing} {
		if _, ok := readOrMissing(t, filepath.Join(root, filepath.FromSlash(note))); ok {
			t.Fatalf("the moved note was left at %s", note)
		}
	}
	for note, comments := range map[string]string{"inbox/A.md": testComments, "inbox/B.md": commentsB} {
		if got, ok := readOrMissing(t, commentsFile(root, note)); !ok || got != comments {
			t.Fatalf("the comments of %s did not come back", note)
		}
	}
	if _, ok := readOrMissing(t, commentsFile(root, "archive/A.md")); ok {
		t.Fatal("A's comments were left at the destination")
	}
	if runs, err := v.ListWorkflowRuns(); err != nil || len(runs) != 0 {
		t.Fatalf("a clean rollback left a record: %v %+v", err, runs)
	}
}

// A ledger the desktop wrote carries comments and creation-date entries alike;
// the server puts both back at the paths it derives from the note.
func TestUndoRestoresDesktopSidecarLedger(t *testing.T) {
	v, root := workflowTestVault(t)
	// The run's outcome: the note and its comments at the destination, the
	// creation date file gone.
	if err := os.Remove(filepath.Join(root, "inbox", "A.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "archive"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "archive", "A.md"), []byte("# A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSidecar(t, commentsFile(root, "archive/A.md"), testComments)
	runID := "1700000000000-desktop"
	ledger := `{
  "version": 1,
  "runId": "` + runID + `",
  "workflowId": "desk",
  "startedAt": 1,
  "finishedAt": 2,
  "applied": 1,
  "irreversible": 0,
  "paths": ["inbox/A.md", "archive/A.md"],
  "ops": [{ "kind": "archive", "path": "inbox/A.md" }],
  "journal": [
    { "path": "inbox/A.md", "before": "# A\n" },
    { "path": "archive/A.md", "before": null }
  ],
  "hashes": {},
  "sidecars": [
    { "note": "inbox/A.md", "sidecar": "comments", "before": ` + mustJSON(t, testComments) + `, "after": null },
    { "note": "archive/A.md", "sidecar": "comments", "before": null, "after": "` + *workflowHash(&[]string{testComments}[0]) + `" },
    { "note": "inbox/A.md", "sidecar": "metadata", "before": "{\"version\":1,\"createdAt\":5}\n", "after": null }
  ],
  "undone": false
}
`
	writeSidecar(t, filepath.Join(root, ".zennotes", "workflows", ".runs", runID+".json"), ledger)

	undo, err := v.UndoWorkflowRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if undo.Restored != 2 || len(undo.DriftedPaths) != 0 {
		t.Fatalf("undo = %+v", undo)
	}
	if got, ok := readOrMissing(t, commentsFile(root, "inbox/A.md")); !ok || got != testComments {
		t.Fatal("the comments did not come back")
	}
	if _, ok := readOrMissing(t, commentsFile(root, "archive/A.md")); ok {
		t.Fatal("the comments were left at the destination")
	}
	if got, ok := readOrMissing(t, filepath.Join(root, ".zennotes", "note-metadata", "inbox", "A.md.metadata.json")); !ok || got != "{\"version\":1,\"createdAt\":5}\n" {
		t.Fatal("the desktop's creation-date file did not come back")
	}
	if got, ok := readOrMissing(t, filepath.Join(root, "inbox", "A.md")); !ok || got != "# A\n" {
		t.Fatal("the note did not come back")
	}
}

func TestUndoNamesDriftedCommentsUnderTheNote(t *testing.T) {
	v, root := workflowTestVault(t)
	writeSidecar(t, commentsFile(root, "inbox/A.md"), testComments)
	receipt, err := v.ApplyPreparedWorkflow(archiveRun(t))
	if err != nil || receipt.RolledBack != nil {
		t.Fatalf("apply: %v %+v", err, receipt)
	}
	// A reply added to the moved note since the run: undo takes it away and
	// says so under the note's name, as it does for an edited note.
	writeSidecar(t, commentsFile(root, "archive/A.md"), "{\"version\":1,\"comments\":[{\"id\":\"new\",\"body\":\"since\"}]}")

	undo, err := v.UndoWorkflowRun(receipt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(undo.DriftedPaths) != 1 || undo.DriftedPaths[0] != "archive/A.md" {
		t.Fatalf("drifted = %v", undo.DriftedPaths)
	}
	if got, ok := readOrMissing(t, commentsFile(root, "inbox/A.md")); !ok || got != testComments {
		t.Fatal("the recorded comments were not restored")
	}
}

func TestSidecarEntryCannotEscapeTheNotes(t *testing.T) {
	v, root := workflowTestVault(t)
	runID := "1700000000000-forged"
	ledger := `{
  "version": 1,
  "runId": "` + runID + `",
  "workflowId": "forged",
  "startedAt": 1,
  "finishedAt": 2,
  "applied": 1,
  "irreversible": 0,
  "paths": ["inbox/A.md"],
  "ops": [],
  "journal": [{ "path": "inbox/A.md", "before": "# A\n" }],
  "hashes": {},
  "sidecars": [
    { "note": "../escaped.md", "sidecar": "comments", "before": "owned" },
    { "note": ".zennotes/workflows/flow.md", "sidecar": "metadata", "before": "owned" }
  ],
  "undone": false
}
`
	writeSidecar(t, filepath.Join(root, ".zennotes", "workflows", ".runs", runID+".json"), ledger)

	_, err := v.UndoWorkflowRun(runID)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("err = %v", err)
	}
	for _, abs := range []string{
		filepath.Join(root, ".zennotes", "escaped.md.comments.json"),
		filepath.Join(root, "..", "escaped.md.comments.json"),
		filepath.Join(root, ".zennotes", "note-metadata", ".zennotes", "workflows", "flow.md.metadata.json"),
	} {
		if _, ok := readOrMissing(t, abs); ok {
			t.Fatalf("a forged sidecar entry wrote %s", abs)
		}
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
