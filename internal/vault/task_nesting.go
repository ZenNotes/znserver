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
// following spaces, one past more than four, none after a marker that ends
// the line), blank lines never close an item, a plain bullet in between owns
// the task under it, a glued checkbox (`- [x]glued`) is prose, blockquotes
// nest by their own `>` markers and a blank line ends them, and a fence is a
// block at its own indent.
//
// Lazy continuation: while an item's text (a paragraph) is open, a line that
// cannot start a block of its own continues the text however little it is
// indented, so it closes nothing; `- [ ] parent`, `wraps here` at the margin,
// then `  - [ ] child` keeps child a subtask, as the chip does. What may cut
// the text short is what Lezer's Markdown parser (the chip's) lets interrupt
// a paragraph: a blank line, a heading, a fence, a `>` the text is not
// inside, a rule, an HTML block, a filled bullet (an empty one only inside a
// bulleted list) and a filled `1.` (any number only inside a numbered list).
// A setext underline (`===`) inside the item ends its text, and an empty
// item, or a lone `>` that starts a quote, opens text the next line can
// continue (a lone `>` inside an open quote is a blank line). A line four
// columns past the item it sits in is text while the text is open and code
// after a blank line: never a list item. Tabs advance to the next multiple
// of four.

var nestingListItemRe = regexp.MustCompile(`^([ \t]*)([-+*]|\d+[.)])([ \t]+|$)`)

// Block starts, read from a line's first character that is not a space or a
// tab: Lezer's isAtxHeading, isFencedCode, isBulletList, isOrderedList,
// isHorizontalRule, isSetextUnderline and isHTMLBlock, spelled as the
// TypeScript spells them.
var (
	nestingHeadingRe      = regexp.MustCompile(`^#{1,6}(?: |$)`)
	nestingFenceRe        = regexp.MustCompile("^(?:```|~~~)")
	nestingBulletRe       = regexp.MustCompile(`^[-+*](?:[ \t]|$)`)
	nestingFilledBulletRe = regexp.MustCompile(`^[-+*][ \t]+[^ \t]`)
	nestingNumberRe       = regexp.MustCompile(`^\d{1,9}[.)](?:[ \t]|$)`)
	nestingFilledOneRe    = regexp.MustCompile(`^1[.)][ \t]+[^ \t]`)
	nestingRuleRe         = regexp.MustCompile(`^(?:(?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	nestingUnderlineRe    = regexp.MustCompile(`^(?:=+|-+)[ \t]*$`)
	// HTML blocks that may interrupt a paragraph (CommonMark's types 1 to 6).
	nestingHTMLInterruptingRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^<(?:script|pre|style)(?:[ \t]|>|$)`),
		regexp.MustCompile(`^<!--`),
		regexp.MustCompile(`^<\?`),
		regexp.MustCompile(`^<![A-Z]`),
		regexp.MustCompile(`^<!\[CDATA\[`),
		regexp.MustCompile(`(?i)^</?(?:address|article|aside|base|basefont|blockquote|body|caption|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|figure|footer|form|frame|frameset|h1|h2|h3|h4|h5|h6|head|header|hr|html|iframe|legend|li|link|main|menu|menuitem|nav|noframes|ol|optgroup|option|p|param|section|source|summary|table|tbody|td|tfoot|th|thead|title|tr|track|ul)(?:[ \t]|/?>|$)`),
	}
	// The HTML block that starts only where no paragraph is open (type 7): a
	// complete tag alone on its line.
	nestingHTMLTagLineRe = regexp.MustCompile(`(?i)^(?:</[a-z][\w-]*[ \t]*>|<[a-z][\w-]*(?:[ \t]+[a-z:_][\w.-]*(?:[ \t]*=[ \t]*(?:[^ \t"'=<>` + "`" + `]+|'[^']*'|"[^"]*"))?)*[ \t]*>)[ \t]*$`)
)

func nestingHTMLInterrupts(text string) bool {
	for _, re := range nestingHTMLInterruptingRes {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// nestingStartsOtherBlock reports whether a line that opens no paragraph
// starts some other block that is not a list item: a heading, a fence, a rule
// or HTML.
func nestingStartsOtherBlock(text string) bool {
	return nestingHeadingRe.MatchString(text) ||
		nestingFenceRe.MatchString(text) ||
		nestingRuleRe.MatchString(text) ||
		nestingHTMLInterrupts(text) ||
		nestingHTMLTagLineRe.MatchString(text)
}

// nestingBlank reads blank as the TypeScript's /^\s*$/ does (U+FEFF counts,
// U+0085 does not), so both sides close the same items.
func nestingBlank(text string) bool {
	return strings.TrimFunc(text, isJSWhitespace) == ""
}

type nestingItem struct {
	depth      int    // blockquote depth the item lives at
	contentCol int    // column a line must reach to stay inside the item
	taskID     string // empty for a plain bullet or a glued checkbox
	ordered    bool   // a numbered item: what may interrupt text depends on it
}

type taskNesting struct {
	open []nestingItem
	// The quote depth of the text (paragraph) the lines so far leave open,
	// when hasParagraph; none after a blank line, a heading, a fence, a rule
	// or code.
	paragraph    int
	hasParagraph bool
	// How many quotes the lines so far leave open.
	quotes int
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

// quotesStayInside reports whether each `>` sits inside the items of the
// level it opens from; one that does not starts a quote outside them.
func (n *taskNesting) quotesStayInside(markers []int) bool {
	for d := range markers {
		for _, item := range n.open {
			if item.depth == d && item.contentCol > markers[d] {
				return false
			}
		}
	}
	return true
}

// baseFor is the content column of the deepest item at quote depth `depth`
// a line at `col` is still inside (items are open outermost first), or 0.
func (n *taskNesting) baseFor(depth, col int) int {
	base := 0
	for _, item := range n.open {
		if item.depth != depth {
			continue
		}
		if item.contentCol > col {
			break
		}
		base = item.contentCol
	}
	return base
}

func (n *taskNesting) inList(ordered bool) bool {
	for _, item := range n.open {
		if item.ordered == ordered {
			return true
		}
	}
	return false
}

// interrupts reports whether text may cut an open paragraph short (Lezer's
// endLeafBlock): a bullet only when filled or inside a bulleted list, a number
// only as a filled `1.` or inside a numbered list.
func (n *taskNesting) interrupts(text string) bool {
	if nestingHeadingRe.MatchString(text) || nestingFenceRe.MatchString(text) || nestingRuleRe.MatchString(text) {
		return true
	}
	if nestingHTMLInterrupts(text) {
		return true
	}
	if nestingBulletRe.MatchString(text) {
		return nestingFilledBulletRe.MatchString(text) || n.inList(false)
	}
	if nestingNumberRe.MatchString(text) {
		return nestingFilledOneRe.MatchString(text) || n.inList(true)
	}
	return false
}

type paragraphMeeting int

const (
	paragraphEnds paragraphMeeting = iota
	paragraphContinues
	paragraphUnderlined
)

// meetParagraph says what a non-blank line does to the open paragraph: it
// goes on with the text (lazily, when it is indented less than the item
// holding the text), underlines it (which ends it), or ends it by starting
// something else.
func (n *taskNesting) meetParagraph(markers []int, col int, text string) paragraphMeeting {
	if !n.hasParagraph {
		return paragraphEnds
	}
	depth := len(markers)
	// A `>` past the text's own quotes, or one outside the items of its
	// level, starts a quote, which interrupts.
	if depth > n.paragraph || !n.quotesStayInside(markers) {
		return paragraphEnds
	}
	if col-n.baseFor(depth, col) >= 4 {
		return paragraphContinues
	}
	if n.interrupts(text) {
		return paragraphEnds
	}
	if depth == n.paragraph && nestingUnderlineRe.MatchString(text) {
		// Only a line inside the item holding the text underlines it; a lazy
		// `===` is more text.
		holder := -1
		for i := len(n.open) - 1; i >= 0; i-- {
			if n.open[i].depth == depth {
				holder = i
				break
			}
		}
		if holder < 0 || col >= n.open[holder].contentCol {
			return paragraphUnderlined
		}
	}
	return paragraphContinues
}

// visit takes every line outside a fence, in order (an opening fence line
// too, the lines inside it not). taskID is set for a task line Markdown would
// draw as a checkbox. It returns the id of the task this line's list item
// sits directly inside, or "".
func (n *taskNesting) visit(line, taskID string) string {
	markers, rest := nestingSplitQuotes(line)
	depth := len(markers)
	blank := nestingBlank(rest)
	var item []string
	if !blank {
		item = nestingListItemRe.FindStringSubmatch(rest)
	}
	var col int
	if item != nil {
		col = nestingColumnsAfter(item[1], 0)
	} else {
		indent := len(rest) - len(strings.TrimLeft(rest, " \t"))
		col = nestingColumnsAfter(rest[:indent], 0)
	}
	text := strings.TrimLeft(rest, " \t")

	if !blank {
		// More text closes nothing and opens nothing, even when it looks like
		// a list item (`2) more`, a line indented deep under the item).
		switch n.meetParagraph(markers, col, text) {
		case paragraphContinues:
			return ""
		case paragraphUnderlined:
			n.hasParagraph = false
			return ""
		}
	}

	// A `>` past the quotes already open, or outside the items of its level,
	// starts a quote of its own.
	startsQuote := depth > n.quotes || !n.quotesStayInside(markers)
	n.quotes = depth
	// Quotes this line is not inside are over.
	for len(n.open) > 0 && n.open[len(n.open)-1].depth > depth {
		n.open = n.open[:len(n.open)-1]
	}
	// Each `>` closes the items of the level it opens from that it is not
	// indented into.
	for d := 0; d < depth; d++ {
		n.truncateDepth(d, markers[d])
	}
	if blank {
		// A lone `>` inside an open quote is a blank line in it. One that
		// starts a quote opens its (empty) text, as an empty item does.
		n.hasParagraph = startsQuote
		n.paragraph = depth
		return ""
	}

	for len(n.open) > 0 {
		top := n.open[len(n.open)-1]
		if top.depth != depth || top.contentCol <= col {
			break
		}
		n.open = n.open[:len(n.open)-1]
	}
	inner := -1
	if len(n.open) > 0 && n.open[len(n.open)-1].depth == depth {
		inner = len(n.open) - 1
	}
	innerCol := 0
	if inner >= 0 {
		innerCol = n.open[inner].contentCol
	}
	// Four columns past the item it sits in, with no text open: code.
	if col-innerCol >= 4 {
		n.hasParagraph = false
		return ""
	}
	if item == nil {
		n.hasParagraph = !nestingStartsOtherBlock(text)
		n.paragraph = depth
		return ""
	}

	parent := ""
	if inner >= 0 {
		parent = n.open[inner].taskID
	}
	afterMarker := col + len(item[2])
	afterSpaces := nestingColumnsAfter(item[3], afterMarker)
	spaces := afterSpaces - afterMarker
	// A marker ending the line (an empty item) takes content from the column
	// right after it, as Lezer's getListIndent counts.
	contentCol := afterSpaces
	if spaces > 4 {
		contentCol = afterMarker + 1
	}
	ordered := item[2] != "-" && item[2] != "+" && item[2] != "*"
	n.open = append(n.open, nestingItem{depth: depth, contentCol: contentCol, taskID: taskID, ordered: ordered})
	// The item's first line opens its text unless it starts another block
	// there, or code (more than four spaces after the marker). An empty item
	// opens text as well: Lezer lets the next line continue it.
	content := rest[len(item[0]):]
	code := spaces > 4 && !nestingBlank(content)
	n.hasParagraph = !code && !nestingStartsOtherBlock(content)
	n.paragraph = depth
	if taskID == "" {
		return ""
	}
	return parent
}
