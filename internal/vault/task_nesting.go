package vault

import (
	"regexp"
	"strings"
)

// Which task a task line is a subtask of (ZenNotes/zennotes#1028), read from
// the list structure Markdown gives the note. Mirrors createTaskNesting in
// packages/shared-domain/src/task-nesting.ts; both run the cases in
// testdata/task-subtasks.json (vendored from the app repository's
// bridge-contract fixtures), so change them together.
//
// The app's `2/5` chip on a parent task line counts subtasks from the parsed
// Markdown tree. The Kanban board gets flat task records instead, so this
// walks the lines with the CommonMark rules that decide nesting and the card
// shows the fraction the note does: a later line stays in a list item only
// when indented to the item's content column (the marker plus one to four
// following spaces), blank lines never close an item, a plain bullet in
// between owns the task under it, a glued checkbox (`- [x]glued`) is prose,
// blockquotes nest by their own `>` markers and a blank line ends them, and a
// fence is a block at its own indent. Lazy continuation lines are not
// modelled. Tabs advance to the next multiple of four.

var nestingListItemRe = regexp.MustCompile(`^([ \t]*)([-+*]|\d+[.)])([ \t]+|$)`)

type nestingItem struct {
	depth      int    // blockquote depth the item lives at
	contentCol int    // column a line must reach to stay inside the item
	taskID     string // empty for a plain bullet or a glued checkbox
}

type taskNesting struct {
	open []nestingItem
}

func nestingAdvance(col int, ch byte) int {
	if ch == '\t' {
		return col + 4 - col%4
	}
	return col + 1
}

// nestingColumnsAfter is the column `text` reaches when it starts at `from`.
// Only spaces and tabs are ever measured, so bytes are columns.
func nestingColumnsAfter(text string, from int) int {
	col := from
	for i := 0; i < len(text); i++ {
		col = nestingAdvance(col, text[i])
	}
	return col
}

// nestingSplitQuotes returns the column of each `>` marker inside the level
// it opens from, and the rest of the line (one space or tab after each `>`
// belongs to the marker).
func nestingSplitQuotes(line string) ([]int, string) {
	markers := []int{}
	pos := 0
	for {
		i := pos
		col := 0
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			col = nestingAdvance(col, line[i])
			i++
		}
		if i >= len(line) || line[i] != '>' {
			break
		}
		markers = append(markers, col)
		i++
		if i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		pos = i
	}
	return markers, line[pos:]
}

// isMarkdownCheckbox reports whether taskLineRe's group 3 (which starts at the
// `]`) is a checkbox Markdown would draw: `]` then whitespace or the end.
func isMarkdownCheckbox(tail string) bool {
	return len(tail) == 1 || (len(tail) > 1 && (tail[1] == ' ' || tail[1] == '\t'))
}

func (n *taskNesting) truncateDepth(d, markerCol int) {
	for i, item := range n.open {
		if item.depth == d && item.contentCol > markerCol {
			n.open = n.open[:i]
			return
		}
	}
}

// visit takes every line outside a fence, in order (an opening fence line
// too, the lines inside it not). taskID is set for a task line Markdown would
// draw as a checkbox. It returns the id of the task this line's list item
// sits directly inside, or "".
func (n *taskNesting) visit(line, taskID string) string {
	markers, rest := nestingSplitQuotes(line)
	depth := len(markers)

	// Quotes this line is not inside are over.
	for len(n.open) > 0 && n.open[len(n.open)-1].depth > depth {
		n.open = n.open[:len(n.open)-1]
	}
	// Each `>` closes the items of the level it opens from that it is not
	// indented into.
	for d := 0; d < depth; d++ {
		n.truncateDepth(d, markers[d])
	}
	// Blank as the TypeScript's /^\s*$/ reads it (U+FEFF counts, U+0085
	// does not), so both sides close the same items.
	if strings.TrimFunc(rest, isJSWhitespace) == "" {
		return ""
	}

	item := nestingListItemRe.FindStringSubmatch(rest)
	var col int
	if item != nil {
		col = nestingColumnsAfter(item[1], 0)
	} else {
		indent := len(rest) - len(strings.TrimLeft(rest, " \t"))
		col = nestingColumnsAfter(rest[:indent], 0)
	}
	for len(n.open) > 0 {
		top := n.open[len(n.open)-1]
		if top.depth != depth || top.contentCol <= col {
			break
		}
		n.open = n.open[:len(n.open)-1]
	}
	if item == nil {
		return ""
	}

	parent := ""
	if len(n.open) > 0 {
		if top := n.open[len(n.open)-1]; top.depth == depth {
			parent = top.taskID
		}
	}
	afterMarker := col + len(item[2])
	afterSpaces := nestingColumnsAfter(item[3], afterMarker)
	contentCol := afterSpaces
	if spaces := afterSpaces - afterMarker; spaces == 0 || spaces > 4 {
		contentCol = afterMarker + 1
	}
	n.open = append(n.open, nestingItem{depth: depth, contentCol: contentCol, taskID: taskID})
	if taskID == "" {
		return ""
	}
	return parent
}
