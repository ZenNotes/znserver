package vault

import "testing"

func TestBodyHasLocalAssetDetectsOnlyLocalAssets(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "plain wikilink",
			body: "# Plain\n\n[[Project Note]]\n",
			want: false,
		},
		{
			name: "relative image",
			body: "# Image\n\n![diagram](../attachements/diagram.png)\n",
			want: true,
		},
		{
			name: "embedded pdf",
			body: "# Embed\n\n![[brief.pdf]]\n",
			want: true,
		},
		{
			name: "remote image",
			body: "# Remote\n\n![diagram](https://example.com/diagram.png)\n",
			want: false,
		},
		{
			name: "code fenced local asset",
			body: "# Code\n\n```md\n![diagram](local.png)\n```\n",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BodyHasLocalAsset(tc.body); got != tc.want {
				t.Fatalf("BodyHasLocalAsset() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtractorsStillIgnoreCodeAfterFastPathGuards(t *testing.T) {
	body := "# Real #tag\n\n```md\n#ignored [[Ignored]] ![[ignored.pdf]]\n```\n\n[[Target|Label]]"

	tags := ExtractTags(body)
	if len(tags) != 1 || tags[0] != "tag" {
		t.Fatalf("ExtractTags() = %#v, want [tag]", tags)
	}

	wikilinks := ExtractWikilinks(body)
	if len(wikilinks) != 1 || wikilinks[0] != "Target" {
		t.Fatalf("ExtractWikilinks() = %#v, want [Target]", wikilinks)
	}
}

// #293: a fenced code block nested under a list item (indented) is still code —
// its `#include` line must not be indexed as a tag.
func TestExtractTagsIgnoresIndentedFence(t *testing.T) {
	body := "- bullet\n\n  ```c\n  #include <stdio.h>\n  ```\n\n#kept"

	tags := ExtractTags(body)
	if len(tags) != 1 || tags[0] != "kept" {
		t.Fatalf("ExtractTags() = %#v, want [kept]", tags)
	}
}

func TestExtractTagsIncludesFrontmatterTags(t *testing.T) {
	body := "---\ntags: [frontmatter, \"#quoted\", project/nested]\ntitle: #ignored\n---\n\n#inline"

	tags := ExtractTags(body)
	want := []string{"frontmatter", "quoted", "project/nested", "inline"}
	if len(tags) != len(want) {
		t.Fatalf("ExtractTags() = %#v, want %#v", tags, want)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("ExtractTags() = %#v, want %#v", tags, want)
		}
	}
}

func TestExtractTagsIncludesFrontmatterTagList(t *testing.T) {
	body := "---\ntags:\n  - daily\n  - \"#log\"\n---\n\nBody"

	tags := ExtractTags(body)
	if len(tags) != 2 || tags[0] != "daily" || tags[1] != "log" {
		t.Fatalf("ExtractTags() = %#v, want [daily log]", tags)
	}
}

// #205: tags in non-Latin scripts (Cyrillic, CJK, …) must be recognized.
func TestExtractTagsUnicode(t *testing.T) {
	body := "Заметки: #тест #ошибка/баг и 笔记 #标签 plus #ascii-1 done"
	got := ExtractTags(body)
	want := map[string]bool{"тест": true, "ошибка/баг": true, "标签": true, "ascii-1": true}
	if len(got) != len(want) {
		t.Fatalf("ExtractTags() = %#v, want keys %#v", got, want)
	}
	for _, tag := range got {
		if !want[tag] {
			t.Fatalf("unexpected tag %q in %#v", tag, got)
		}
	}
}

// #450: `[-]` cancelled tasks must be parsed (not dropped) and flagged cancelled.
func TestParseTasksRecognizesCancelled(t *testing.T) {
	body := "- [ ] open\n- [x] done\n- [>] gone\n- [-] scrapped\n"
	tasks := ParseTasks("inbox/t.md", "t", FolderInbox, body)
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks (none dropped), got %d", len(tasks))
	}
	byContent := map[string]Task{}
	for _, tk := range tasks {
		byContent[tk.Content] = tk
	}
	if c, ok := byContent["scrapped"]; !ok {
		t.Fatal("cancelled task line was dropped")
	} else if !c.Cancelled || c.Checked {
		t.Errorf("scrapped: Cancelled=%v Checked=%v, want Cancelled=true Checked=false", c.Cancelled, c.Checked)
	}
	if byContent["open"].Cancelled || byContent["done"].Cancelled {
		t.Error("open/done tasks should not be cancelled")
	}
}

func TestParseTaskFileCancelledStatus(t *testing.T) {
	body := "---\ntags: [task]\ntitle: Rewrite\nstatus: cancelled\n---\n\nAbandoned.\n"
	task, ok := parseTaskFile("inbox/x.md", "x", FolderInbox, body)
	if !ok {
		t.Fatal("expected a file task")
	}
	if !task.Cancelled || task.Checked {
		t.Errorf("Cancelled=%v Checked=%v, want Cancelled=true Checked=false", task.Cancelled, task.Checked)
	}
}
