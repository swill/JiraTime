package main

import (
	"encoding/json"
	"testing"
)

func parseADF(t *testing.T, doc string) *adfNode {
	t.Helper()
	var n adfNode
	if err := json.Unmarshal([]byte(doc), &n); err != nil {
		t.Fatalf("parse %s: %v", doc, err)
	}
	return &n
}

// Everything written in Jira that has a text form shows in the description
func TestADFTextShowsTheWholeComment(t *testing.T) {
	doc := parseADF(t, `{"type":"doc","version":1,"content":[
		{"type":"paragraph","content":[
			{"type":"text","text":"Fixed the "},
			{"type":"text","text":"login","marks":[{"type":"strong"}]},
			{"type":"text","text":" bug with "},
			{"type":"mention","attrs":{"id":"abc","text":"@Tara Target"}},
			{"type":"text","text":" "},
			{"type":"emoji","attrs":{"shortName":":tada:","text":"🎉"}}
		]},
		{"type":"paragraph","content":[
			{"type":"text","text":"See "},
			{"type":"text","text":"the runbook","marks":[{"type":"link","attrs":{"href":"https://wiki.example/runbook"}}]},
			{"type":"text","text":" and "},
			{"type":"inlineCard","attrs":{"url":"https://jira.example/browse/ABC-2"}},
			{"type":"hardBreak"},
			{"type":"text","text":"Due "},
			{"type":"date","attrs":{"timestamp":"1760659200000"}},
			{"type":"text","text":", "},
			{"type":"status","attrs":{"text":"IN PROGRESS","color":"blue"}}
		]},
		{"type":"paragraph"},
		{"type":"heading","attrs":{"level":3},"content":[{"type":"text","text":"Steps"}]},
		{"type":"orderedList","attrs":{"order":1},"content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Reproduced"}]}]},
			{"type":"listItem","content":[
				{"type":"paragraph","content":[{"type":"text","text":"Patched"}]},
				{"type":"bulletList","content":[
					{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"auth.go"}]}]}
				]}
			]}
		]},
		{"type":"taskList","attrs":{"localId":"t"},"content":[
			{"type":"taskItem","attrs":{"localId":"1","state":"DONE"},"content":[{"type":"text","text":"Deploy"}]},
			{"type":"taskItem","attrs":{"localId":"2","state":"TODO"},"content":[{"type":"text","text":"Tell support"}]}
		]},
		{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"if err != nil {\n\treturn err\n}"}]},
		{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"Quoted"}]}]},
		{"type":"rule"},
		{"type":"panel","attrs":{"panelType":"info"},"content":[{"type":"paragraph","content":[{"type":"text","text":"In a panel"}]}]},
		{"type":"expand","attrs":{"title":"Details"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Hidden text"}]}]},
		{"type":"table","content":[
			{"type":"tableRow","content":[
				{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Host"}]}]},
				{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Result"}]}]}
			]},
			{"type":"tableRow","content":[
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"web1"}]}]},
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"ok"}]}]}
			]}
		]},
		{"type":"mediaSingle","attrs":{"layout":"center"},"content":[{"type":"media","attrs":{"id":"1","type":"file","collection":"c","alt":"screenshot.png"}}]},
		{"type":"mediaGroup","content":[{"type":"media","attrs":{"id":"2","type":"file","collection":"c"}}]},
		{"type":"extension","attrs":{"extensionType":"com.atlassian","extensionKey":"toc"}},
		{"type":"paragraph","content":[{"type":"text","text":"Line with\r\nWindows ending"}]}
	]}`)

	want := "Fixed the login bug with @Tara Target 🎉\n" +
		"See the runbook (https://wiki.example/runbook) and https://jira.example/browse/ABC-2\n" +
		"Due 2025-10-17, IN PROGRESS\n" +
		"\n" +
		"Steps\n" +
		"1. Reproduced\n" +
		"2. Patched\n" +
		"   • auth.go\n" +
		"[x] Deploy\n" +
		"[ ] Tell support\n" +
		"if err != nil {\n\treturn err\n}\n" +
		"> Quoted\n" +
		"---\n" +
		"In a panel\n" +
		"Details\n" +
		"Hidden text\n" +
		"Host | Result\n" +
		"web1 | ok\n" +
		"[attachment: screenshot.png]\n" +
		"[attachment]\n" +
		"[extension]\n" +
		"Line with\nWindows ending"

	if got := adfText(doc); got != want {
		t.Errorf("got:\n%s\n\nwant:\n%s", got, want)
	}
	if !adfFormatted(doc) {
		t.Error("formatted comment not flagged as formatted")
	}
}

func TestADFTextOfPlainComments(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"one line", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Standup"}]}]}`, "Standup"},
		{"paragraphs", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"One"}]},{"type":"paragraph","content":[{"type":"text","text":"Two"}]}]}`, "One\nTwo"},
		{"line break", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"One"},{"type":"hardBreak"},{"type":"text","text":"Two"}]}]}`, "One\nTwo"},
		{"leading and trailing blank paragraphs", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[]},{"type":"paragraph","content":[{"type":"text","text":"Text"}]},{"type":"paragraph"}]}`, "Text"},
		{"empty", `{"type":"doc","version":1,"content":[]}`, ""},
	}
	for _, c := range cases {
		doc := parseADF(t, c.doc)
		if got := adfText(doc); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		if adfFormatted(doc) {
			t.Errorf("%s: plain comment flagged as formatted", c.name)
		}
	}
	if got := adfText(nil); got != "" {
		t.Errorf("no comment: got %q, want empty", got)
	}
}

// A description saved from the dialog reads back exactly as typed, so it
// counts as unedited next time and isn't rewritten
func TestBuildADFCommentRoundTrips(t *testing.T) {
	for _, text := range []string{"Standup", "First line\nSecond line", "Before\n\nAfter a blank line", "  indented\n\ttabbed"} {
		raw, err := json.Marshal(buildADFComment(text))
		if err != nil {
			t.Fatal(err)
		}
		doc := parseADF(t, string(raw))
		if got := adfText(doc); got != text {
			t.Errorf("%q came back as %q", text, got)
		}
		if adfFormatted(doc) {
			t.Errorf("%q: plain description flagged as formatted", text)
		}
		for _, p := range doc.Content {
			for _, n := range p.Content {
				if n.Type == "text" && n.Text == "" {
					t.Errorf("%q: empty text node, which Jira rejects", text)
				}
			}
		}
	}
}
