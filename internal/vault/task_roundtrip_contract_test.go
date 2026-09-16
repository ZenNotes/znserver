package vault

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestSharedTaskRoundtripContract(t *testing.T) {
	data, err := os.ReadFile("testdata/task-roundtrip.json")
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
			Body              string         `json:"body"`
			ExpectedBody      string         `json:"expectedBody"`
			TaskIndex         int            `json:"taskIndex"`
			ExpectedBefore    map[string]any `json:"expectedBefore"`
			ExpectedAfter     map[string]any `json:"expectedAfter"`
			ExpectedTaskCount int            `json:"expectedTaskCount"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || len(fixture.Cases) == 0 {
		t.Fatal("unsupported or empty task contract fixture")
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			v, err := New(t.TempDir(), Options{})
			if err != nil {
				t.Fatal(err)
			}
			var originalID string
			// The client transforms Markdown; Go stores those exact bytes and
			// parses the resulting task state for the next client read.
			for index, phase := range []struct {
				body string
				want map[string]any
			}{{tc.Body, tc.ExpectedBefore}, {tc.ExpectedBody, tc.ExpectedAfter}} {
				if _, err := v.WriteNote(tc.Note.Path, phase.body); err != nil {
					t.Fatal(err)
				}
				note, err := v.ReadNote(tc.Note.Path)
				if err != nil {
					t.Fatal(err)
				}
				if note.Body != phase.body {
					t.Fatal("storage changed Markdown bytes")
				}
				tasks := ParseTasks(tc.Note.Path, tc.Note.Title, tc.Note.Folder, note.Body)
				if len(tasks) != tc.ExpectedTaskCount || tc.TaskIndex < 0 || tc.TaskIndex >= len(tasks) {
					t.Fatalf("got %d tasks, want %d with index %d", len(tasks), tc.ExpectedTaskCount, tc.TaskIndex)
				}
				task := tasks[tc.TaskIndex]
				if index == 0 {
					originalID = task.ID
				} else if task.ID != originalID {
					t.Fatal("task identity changed after editing")
				}
				encoded, err := json.Marshal(task)
				if err != nil {
					t.Fatal(err)
				}
				var actual map[string]any
				if err := json.Unmarshal(encoded, &actual); err != nil {
					t.Fatal(err)
				}
				for field, want := range phase.want {
					if !reflect.DeepEqual(actual[field], want) {
						t.Errorf("phase %d field %s: got %#v, want %#v", index, field, actual[field], want)
					}
				}
			}
		})
	}
}
