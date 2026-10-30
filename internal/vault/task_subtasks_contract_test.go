package vault

import (
	"encoding/json"
	"os"
	"testing"
)

// TestSharedTaskSubtasksContract runs the subtask fixture shared with the
// TypeScript parser (ZenNotes/zennotes#1028): for each note body, the whole
// task listing in order, and the parentId each task carries. A task the
// fixture lists without parentId must serialize without one.
func TestSharedTaskSubtasksContract(t *testing.T) {
	data, err := os.ReadFile("testdata/task-subtasks.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int `json:"schemaVersion"`
		Cases         []struct {
			ID   string `json:"id"`
			Note struct {
				Path   string     `json:"path"`
				Title  string     `json:"title"`
				Folder NoteFolder `json:"folder"`
			} `json:"note"`
			Body          string `json:"body"`
			ExpectedTasks []struct {
				ID       string  `json:"id"`
				ParentID *string `json:"parentId"`
			} `json:"expectedTasks"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || len(fixture.Cases) == 0 {
		t.Fatal("unsupported or empty task subtasks fixture")
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			tasks := ParseTasks(tc.Note.Path, tc.Note.Title, tc.Note.Folder, tc.Body)
			if len(tasks) != len(tc.ExpectedTasks) {
				t.Fatalf("got %d tasks, want %d", len(tasks), len(tc.ExpectedTasks))
			}
			for i, want := range tc.ExpectedTasks {
				task := tasks[i]
				if task.ID != want.ID {
					t.Errorf("task %d: id %q, want %q", i, task.ID, want.ID)
				}
				encoded, err := json.Marshal(task)
				if err != nil {
					t.Fatal(err)
				}
				var actual map[string]any
				if err := json.Unmarshal(encoded, &actual); err != nil {
					t.Fatal(err)
				}
				got, present := actual["parentId"]
				switch {
				case want.ParentID == nil && present:
					t.Errorf("task %s: parentId %#v, want none", want.ID, got)
				case want.ParentID != nil && got != *want.ParentID:
					t.Errorf("task %s: parentId %#v, want %q", want.ID, got, *want.ParentID)
				}
			}
		})
	}
}
