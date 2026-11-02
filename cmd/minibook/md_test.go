package main

import (
	"strings"
	"testing"
	"time"
)

// malformed are pages a careless edit upstream could produce. None may
// panic or hang the reader.
var malformed = []string{
	"",
	"\n\n\n",
	"---",
	"---\n",
	"---\ntitle: x",
	"---\ntitle: \"\n---",
	"\ufeff---\r\ntitle: x\r\n---\r\n",
	"```",
	"```go\nfunc() {",
	"~~~~\n```\n~~",
	"<!-- never closed",
	"{/* never closed",
	"a <!-- b --> c <!-- d",
	"<div\nclass=\"x\"",
	"<table>",
	"<table><tr><td>a",
	"<table>\n```\n</table>",
	"<span></span>> quote after a tag",
	"<b></b>> quote after bold",
	"> > > >",
	">",
	":::",
	":::note",
	":::note[",
	"::: \n:::tip\n:::",
	"# ",
	"#",
	"######## too deep",
	"[",
	"[a](",
	"[a](b",
	"![](",
	"![a]()",
	"[[[[[[[[[[a]]]]]]]]]]",
	"**",
	"***a",
	"~~a",
	"_a_b_c_",
	"`",
	"``a`",
	"\\",
	"a\\",
	"| a |\n|---|",
	"|\n|-|",
	"| a | b |\n| - | - |\n| c",
	"- ",
	"1.",
	"-\n  -\n    -\n      - a",
	strings.Repeat("  ", 200) + "- deep",
	strings.Repeat("> ", 300) + "deep quote",
	strings.Repeat(":::note\n", 50),
	strings.Repeat("- a\n  ", 100),
	strings.Repeat("[", 2000) + strings.Repeat("]", 2000),
	strings.Repeat("*", 5000),
	"export const x = {\n",
	"import X from 'y'",
	"<TabItem label=\"\">",
	"<DocCardList/>",
	"\xff\xfe invalid utf-8 \xc3",
	"\u2028\u2028",
}

// parseAll runs a page through everything the reader does with it.
func parseAll(src string) {
	d := Parse(src)
	pg := Layout(d, textW)
	pg.find("a")
	pg.scroll(0, 40, 300)
	pg.pageDown(0, 300)
	pg.pageUp(pg.maxTop(300), 300)
	PageTitle(src)
	snippet(PlainText(d.Blocks), "a")
}

func TestMalformedPages(t *testing.T) {
	for _, src := range malformed {
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%q: panic: %v", src, r)
				}
			}()
			parseAll(src)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%q: still parsing after 5 s", src)
		}
	}
}

// FuzzParse feeds the parser and layout random pages. A short
// -fuzzminimizetime keeps the minimiser from stalling the run on long
// inputs: go test -run=x -fuzz=FuzzParse -fuzzminimizetime=2s ./cmd/minibook
func FuzzParse(f *testing.F) {
	for _, src := range malformed {
		f.Add(src)
	}
	f.Add("---\ntitle: T\n---\n# H {#id}\n\nText with **bold**, `code` and [a link](/docs/x).\n\n:::tip[Title]\n- item\n  > quote\n:::\n\n| a | b |\n|---|---|\n| 1 | 2 |\n")
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 4096 {
			return
		}
		parseAll(src)
	})
}
