package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Worklog comments are stored in Atlassian Document Format (ADF): a tree of
// block nodes (paragraphs, lists, tables, ...) holding inline nodes (text,
// mentions, emoji, ...). The edit dialog shows and edits them as plain text.

// adfNode is one node of an ADF document
type adfNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Marks   []adfMark      `json:"marks,omitempty"`
	Content []adfNode      `json:"content,omitempty"`
}

// adfMark is formatting on a text node (bold, link, ...)
type adfMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// description returns a worklog's whole comment as plain text, which is
// what the edit dialog shows and saves
func (wl *JiraWorklog) description() string {
	return adfText(wl.Comment)
}

// commentFormatted reports whether a worklog's comment holds more than plain
// text can (formatting, mentions, lists, images, ...), which would be lost if
// its description were edited and saved as plain text
func (wl *JiraWorklog) commentFormatted() bool {
	return wl.Comment != nil && adfFormatted(wl.Comment)
}

// adfText renders a comment as plain text: one line per paragraph, keeping
// everything else that has a text form (mentions, emoji, dates, link
// addresses, list markers, code, tables) so nothing written in Jira looks
// lost. Images and files, which have none, show as placeholders.
func adfText(doc *adfNode) string {
	if doc == nil {
		return ""
	}
	var lines []string
	adfBlocks(&lines, doc.Content)
	text := strings.Join(lines, "\n")
	// A textarea turns \r\n and \r into \n, so an unedited description
	// must already match
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Trim(text, "\n")
}

// adfBlocks appends the lines of a list of block nodes
func adfBlocks(lines *[]string, nodes []adfNode) {
	for _, n := range nodes {
		switch n.Type {
		case "paragraph", "heading", "codeBlock":
			*lines = append(*lines, strings.Split(adfInline(n.Content), "\n")...)
		case "bulletList":
			for _, item := range n.Content {
				adfListItem(lines, item, "• ")
			}
		case "orderedList":
			number := 1
			if order, ok := n.Attrs["order"].(float64); ok && order >= 0 {
				number = int(order)
			}
			for _, item := range n.Content {
				adfListItem(lines, item, strconv.Itoa(number)+". ")
				number++
			}
		case "taskList", "decisionList":
			for _, item := range n.Content {
				marker := "• "
				switch {
				case item.Type == "taskItem" && item.Attrs["state"] == "DONE":
					marker = "[x] "
				case item.Type == "taskItem":
					marker = "[ ] "
				}
				adfListItem(lines, item, marker)
			}
		case "blockquote":
			var quoted []string
			adfBlocks(&quoted, n.Content)
			for _, line := range quoted {
				*lines = append(*lines, "> "+line)
			}
		case "rule":
			*lines = append(*lines, "---")
		case "table":
			for _, row := range n.Content {
				var cells []string
				for _, cell := range row.Content {
					var cellLines []string
					adfBlocks(&cellLines, cell.Content)
					cells = append(cells, strings.Join(cellLines, " "))
				}
				*lines = append(*lines, strings.Join(cells, " | "))
			}
		case "mediaSingle", "mediaGroup":
			for _, media := range n.Content {
				*lines = append(*lines, adfMedia(media))
			}
		case "media":
			*lines = append(*lines, adfMedia(n))
		case "blockCard", "embedCard":
			*lines = append(*lines, adfString(n.Attrs, "url"))
		default:
			// Containers (panels, expands, layouts, ...): their title, if
			// any, then what they hold
			before := len(*lines)
			if title := adfString(n.Attrs, "title"); title != "" {
				*lines = append(*lines, title)
			}
			if adfHasBlocks(n.Content) {
				adfBlocks(lines, n.Content)
			} else if text := adfInline([]adfNode{n}); text != "" {
				*lines = append(*lines, strings.Split(text, "\n")...)
			}
			if len(*lines) == before {
				// Nothing with a text form (e.g. a Jira macro): say it's there
				*lines = append(*lines, "["+n.Type+"]")
			}
		}
	}
}

// adfListItem appends a list item: its marker on the first line and the
// following lines (including nested lists) indented to match
func adfListItem(lines *[]string, item adfNode, marker string) {
	var itemLines []string
	if adfHasBlocks(item.Content) {
		adfBlocks(&itemLines, item.Content)
	} else {
		// Task and decision items hold inline nodes directly
		itemLines = strings.Split(adfInline(item.Content), "\n")
	}
	indent := strings.Repeat(" ", len([]rune(marker)))
	for i, line := range itemLines {
		if i == 0 {
			*lines = append(*lines, marker+line)
		} else {
			*lines = append(*lines, indent+line)
		}
	}
}

// adfHasBlocks reports whether nodes are block nodes rather than inline ones
func adfHasBlocks(nodes []adfNode) bool {
	for _, n := range nodes {
		switch n.Type {
		case "text", "hardBreak", "mention", "emoji", "date", "status", "inlineCard", "mediaInline", "placeholder", "inlineExtension":
			return false
		}
	}
	return len(nodes) > 0
}

// adfInline renders inline nodes as text; hard line breaks become \n
func adfInline(nodes []adfNode) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(n.Text)
			// Keep a link's address, unless the text already is it
			for _, m := range n.Marks {
				if href := adfString(m.Attrs, "href"); m.Type == "link" && href != "" && href != n.Text {
					fmt.Fprintf(&b, " (%s)", href)
				}
			}
		case "hardBreak":
			b.WriteString("\n")
		case "mention":
			if text := adfString(n.Attrs, "text"); text != "" {
				b.WriteString(text)
			} else {
				b.WriteString("@user")
			}
		case "emoji":
			if text := adfString(n.Attrs, "text"); text != "" {
				b.WriteString(text)
			} else {
				b.WriteString(adfString(n.Attrs, "shortName"))
			}
		case "date":
			// Milliseconds since the epoch, for a UTC date
			if ms, err := strconv.ParseInt(adfString(n.Attrs, "timestamp"), 10, 64); err == nil {
				b.WriteString(time.UnixMilli(ms).UTC().Format("2006-01-02"))
			}
		case "inlineCard":
			b.WriteString(adfString(n.Attrs, "url"))
		case "mediaInline":
			b.WriteString(adfMedia(n))
		default:
			// status, placeholder and anything newer: its text, else say
			// it's there
			switch text := adfInline(n.Content); {
			case n.Text != "":
				b.WriteString(n.Text)
			case adfString(n.Attrs, "text") != "":
				b.WriteString(adfString(n.Attrs, "text"))
			case text != "":
				b.WriteString(text)
			default:
				fmt.Fprintf(&b, "[%s]", n.Type)
			}
		}
	}
	return b.String()
}

// adfMedia is the placeholder for an image or file, which has no text
func adfMedia(n adfNode) string {
	if url := adfString(n.Attrs, "url"); url != "" {
		return url // an external image
	}
	if alt := adfString(n.Attrs, "alt"); alt != "" {
		return "[attachment: " + alt + "]"
	}
	return "[attachment]"
}

// adfString returns a string attribute, or "" when it is missing
func adfString(attrs map[string]any, key string) string {
	s, _ := attrs[key].(string)
	return s
}

// adfFormatted reports whether a document holds anything besides plain
// paragraphs of unformatted text and line breaks
func adfFormatted(n *adfNode) bool {
	switch n.Type {
	case "doc", "paragraph", "hardBreak":
	case "text":
		if len(n.Marks) > 0 {
			return true
		}
	default:
		return true
	}
	for i := range n.Content {
		if adfFormatted(&n.Content[i]) {
			return true
		}
	}
	return false
}

// buildADFComment turns the dialog's plain-text description into a comment,
// one paragraph per line (as Jira's editor makes one for each Enter)
func buildADFComment(text string) map[string]any {
	lines := strings.Split(text, "\n")
	paragraphs := make([]map[string]any, len(lines))
	for i, line := range lines {
		content := []map[string]any{}
		if line != "" {
			// ADF text nodes can't be empty, so blank lines are empty paragraphs
			content = append(content, map[string]any{"type": "text", "text": line})
		}
		paragraphs[i] = map[string]any{"type": "paragraph", "content": content}
	}
	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": paragraphs,
	}
}
