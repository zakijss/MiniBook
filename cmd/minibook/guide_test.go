package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zakijss/minideck/ui"
)

// kinds lists the kinds of blocks, for compact checks.
func kinds(bs []Block) string {
	names := map[BlockKind]string{Para: "p", Heading: "h", Item: "li", Code: "code", Table: "table", Callout: "callout", Quote: "quote", Rule: "hr", Cards: "cards"}
	var out []string
	for _, b := range bs {
		out = append(out, names[b.Kind])
	}
	return strings.Join(out, " ")
}

func TestFrontMatter(t *testing.T) {
	d := Parse("---\nslug: /emulators/nes\ntitle: \"NES\"\nsidebar_position: 3\ndescription: The NES\n---\n\n# Ignored heading\n\nText.\n")
	if d.Front.Slug != "/emulators/nes" || d.Title != "NES" || !d.Front.HasPos || d.Front.Pos != 3 || d.Front.Desc != "The NES" {
		t.Fatalf("front: %+v title %q", d.Front, d.Title)
	}
	if kinds(d.Blocks) != "p" {
		t.Fatalf("the first # heading should be dropped (it is the title): %s", kinds(d.Blocks))
	}
	d = Parse("---\nslug: /faq\n---\n\n# FAQ ✔\n\nHi")
	if d.Title != "FAQ ✔" {
		t.Fatalf("title from heading: %q", d.Title)
	}
	if f, title := PageTitle("---\nslug: /x\n---\nimport A from 'a';\n\n```\n# not this\n```\n# <sup>x</sup>Real **title**\n"); title != "xReal title" || f.Slug != "/x" {
		t.Fatalf("PageTitle: %q", title)
	}
}

func TestStripMDX(t *testing.T) {
	src := `---
slug: /installation
description: Official installation guide
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';
export const Thing = () => {
  return 1;
};

{frontMatter.description}

<!-- a comment
over two lines -->
## <sup><img align="left" src="https://x/y.png" width="54" /></sup> Check your firmware

*![](https://x/banner.png)*

<img
  title="Nintendo - DS"
  src={require('./assets/nds.webp').default}
  style={{ width: '240px', float: 'right' }}
/>

Press <kbd>MENU</kbd> + <kbd>POWER</kbd>, see <a href="./apps/shortcuts">shortcuts</a>.<br />Next line {/* hidden */} and ` + "`Emu/<systemname>/config.json`" + `.

<Tabs groupId="device" queryString>
<TabItem value="mm" label="Miyoo Mini (MM)">

Text for the Mini.

</TabItem>
</Tabs>

<IconLink href="https://drive.example/fw" icon="cloud_download">Firmware 20220419</IconLink>

<div className="release-links">
    <ReleaseLink showDownloads url="https://api.github.com/x" />
</div>

<details><summary>

### A question?

</summary>

The answer.

</details>

<DocCardList/>

![Roms](https://x/roms.png)
`
	d := Parse(src)
	if got := kinds(d.Blocks); got != "p h p h p p h p cards p" {
		t.Fatalf("blocks: %s\n%#v", got, d.Blocks)
	}
	want := []string{
		"Official installation guide",
		"Check your firmware",
		"Press `MENU` + `POWER`, see [shortcuts](./apps/shortcuts).\u2028Next line  and `Emu/<systemname>/config.json`.",
		"Miyoo Mini (MM)",
		"Text for the Mini.",
		"[Firmware 20220419](https://drive.example/fw)",
		"A question?",
		"The answer.",
		"",
		"![Roms](https://x/roms.png)",
	}
	for i, w := range want {
		if d.Blocks[i].Text != w {
			t.Errorf("block %d: got %q, want %q", i, d.Blocks[i].Text, w)
		}
	}
	if d.Blocks[1].Anchor != "check-your-firmware" || d.Blocks[3].Level != 4 {
		t.Errorf("heading: %+v / %+v", d.Blocks[1], d.Blocks[3])
	}
	txt := PlainText(d.Blocks)
	for _, bad := range []string{"import", "export", "comment", "<", "Tabs", "ReleaseLink", "require", "frontMatter"} {
		if strings.Contains(strings.ReplaceAll(txt, "<systemname>", ""), bad) {
			t.Errorf("%q left in:\n%s", bad, txt)
		}
	}
	if !strings.Contains(txt, "[image: Roms]") {
		t.Errorf("image placeholder missing:\n%s", txt)
	}
}

func TestAdmonitions(t *testing.T) {
	src := `:::tip Windows user?
Try [Onion Desktop Tools](https://github.com/x) for easy SD card preparation.

- one
- two
:::

:::note
Plain note.
:::

:::caution <a href="apps/shortcuts">Shortcuts overview</a>
Careful.

:::info Inner
Nested callout.
:::

After the inner one.
:::

:::danger[Bracket title]
Hot.
:::

1. Step one

   :::warning
   Inside a list item.
   :::

2. Step two
`
	d := Parse(src)
	if got := kinds(d.Blocks); got != "callout callout callout callout li callout li" {
		t.Fatalf("blocks: %s", got)
	}
	tip := d.Blocks[0]
	if tip.Note != "tip" || tip.Text != "Windows user?" || kinds(tip.Kids) != "p li li" {
		t.Errorf("tip: %+v kids %s", tip, kinds(tip.Kids))
	}
	if n := d.Blocks[1]; n.Note != "note" || n.Text != "" || n.Kids[0].Text != "Plain note." {
		t.Errorf("note: %+v", n)
	}
	c := d.Blocks[2]
	if c.Note != "caution" || c.Text != "[Shortcuts overview](apps/shortcuts)" || kinds(c.Kids) != "p callout p" || c.Kids[1].Note != "info" || c.Kids[1].Text != "Inner" {
		t.Errorf("caution: %+v kids %s", c, kinds(c.Kids))
	}
	if b := d.Blocks[3]; b.Note != "danger" || b.Text != "Bracket title" {
		t.Errorf("bracket title: %+v", b)
	}
	if w := d.Blocks[5]; w.Note != "warning" || w.Level != 0 || w.Kids[0].Text != "Inside a list item." {
		t.Errorf("callout in a list: %+v", w)
	}
	// laid out: a box per callout, a default title, the colour of the kind
	pg := Layout(d, 500)
	if len(pg.Boxes) != 6 {
		t.Fatalf("boxes: %d", len(pg.Boxes))
	}
	if pg.Boxes[0].Kind != "tip" {
		t.Errorf("first box is %q", pg.Boxes[0].Kind)
	}
	title := pg.Lines[pg.Boxes[1].From+1]
	if len(title.Runs) == 0 || title.Runs[0].Text != "Note" || title.Runs[0].Color != noteColor("note") {
		t.Errorf("default title: %+v", title)
	}
	inList := pg.Boxes[len(pg.Boxes)-1]
	if inList.X0 != indentStep {
		t.Errorf("a callout in a list item should be indented: %+v", inList)
	}
	// boxes nest: the inner one lies inside the outer one
	var outer, inner Box
	for _, b := range pg.Boxes {
		switch b.Kind {
		case "caution":
			outer = b
		case "info":
			inner = b
		}
	}
	if !(inner.From > outer.From && inner.To < outer.To && inner.X0 > outer.X0) {
		t.Errorf("nesting: outer %+v inner %+v", outer, inner)
	}
}

func TestLists(t *testing.T) {
	d := Parse(strings.ReplaceAll(`- a
- b
  continued
  - nested
    - deeper
- c

1. first
2. second<TWO SPACES>
   hard break
10) tenth

   A paragraph inside the item.

After.
`, "<TWO SPACES>", "  "))
	if got := kinds(d.Blocks); got != "li li li li li li li li li p" {
		t.Fatalf("blocks: %s", got)
	}
	levels := ""
	for _, b := range d.Blocks[:9] {
		levels += fmt.Sprint(b.Level)
	}
	if levels != "001200000" {
		t.Errorf("levels %s", levels)
	}
	if d.Blocks[1].Text != "b continued" || d.Blocks[6].Text != "second\u2028hard break" {
		t.Errorf("continuations: %q %q", d.Blocks[1].Text, d.Blocks[6].Text)
	}
	if d.Blocks[5].Marker != "1." || d.Blocks[7].Marker != "10." || d.Blocks[0].Marker != "•" {
		t.Errorf("markers: %q %q %q", d.Blocks[5].Marker, d.Blocks[7].Marker, d.Blocks[0].Marker)
	}
	if d.Blocks[8].Marker != "" || d.Blocks[8].Text != "A paragraph inside the item." {
		t.Errorf("item paragraph: %+v", d.Blocks[8])
	}
}

func TestTables(t *testing.T) {
	d := Parse("| Button | Function |\n| ------ | -------- |\n| <kbd>A</kbd> | Resume \\| play |\n| `x|y` | Quit |\n\n" +
		`<table align="center">
<tbody>
<tr>
<a href="#shortcuts"><img title="Shortcuts" src="https://x/s.png" width="48" /></a>
</tr>
</tbody>
</table>

<table>
    <thead>
        <tr><th>Button combination</th><th>Action</th></tr>
    </thead>
    <tbody>
        <tr>
            <td><kbd>MENU</kbd> <sup>hold</sup></td>
            <td>Open context menu <sub>[by default]</sub></td>
        </tr>
        <tr class="table-separator"><td colspan="2"><hr /></td></tr>
    </tbody>
</table>
`)
	if got := kinds(d.Blocks); got != "table table" {
		t.Fatalf("blocks: %s", got)
	}
	md := d.Blocks[0]
	if len(md.Rows) != 3 || md.Rows[1][0] != "`A`" || md.Rows[1][1] != "Resume | play" || md.Rows[2][0] != "`x|y`" || !md.Head[0] || md.Head[1] {
		t.Errorf("markdown table: %q %v", md.Rows, md.Head)
	}
	h := d.Blocks[1]
	if len(h.Rows) != 2 || h.Rows[0][0] != "Button combination" || !h.Head[0] || h.Rows[1][0] != "`MENU` hold" || h.Rows[1][1] != "Open context menu [by default]" {
		t.Errorf("html table: %q %v", h.Rows, h.Head)
	}
	// laid out, a table stays inside the column, with a rule between rows
	pg := Layout(d, 300)
	rules := 0
	for _, l := range pg.Lines {
		for _, r := range l.Runs {
			if r.X+r.W > 300 {
				t.Errorf("cell text past the column: %+v", r)
			}
		}
		if l.Rule[1] > l.Rule[0] {
			rules++
			if l.Rule[1] > 300 {
				t.Errorf("rule too wide: %v", l.Rule)
			}
		}
	}
	if rules != 3 {
		t.Errorf("rules: %d", rules)
	}
}

func TestInline(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"plain **bold** and *em* and `co*de*`", "plain |B:bold| and em and |M:co*de*|"},
		{"a [link **x**](apps/x \"title\") b", "a |L(apps/x):link |BL(apps/x):x| b"},
		{"*![](https://x/y.png)*", ""},
		{"![A picture](a.png) after", "|F:[image: A picture]| after"},
		{"[![Alt](img.png)](https://site)", "|FL(https://site):[image: Alt]|"},
		{`snake_case_name and _em_ and \*not em\*`, "snake_case_name and em and *not em*"},
		{"<https://example.com/x> &amp; 2 * 3", "|L(https://example.com/x):https://example.com/x| & 2 * 3"},
		{"``a ` b`` ~~gone~~", "|M:a ` b| gone"},
		{"unclosed **bold and [bracket", "unclosed **bold and [bracket"},
	} {
		var b strings.Builder
		for _, sp := range Inline(tc.in) {
			tag := ""
			if sp.Style&Bold != 0 {
				tag += "B"
			}
			if sp.Style&Mono != 0 {
				tag += "M"
			}
			if sp.Style&Faded != 0 {
				tag += "F"
			}
			if sp.Link != "" {
				tag += "L(" + sp.Link + ")"
			}
			if tag != "" {
				b.WriteString("|" + tag + ":" + sp.Text + "|")
			} else {
				b.WriteString(sp.Text)
			}
		}
		got := strings.ReplaceAll(b.String(), "||", "|")
		if got != tc.want {
			t.Errorf("%q:\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitize(t *testing.T) {
	got := sanitize(ui.Body, "OK ✔\ufe0f warn ⚠\ufe0f files 📁 heart ❤\ufe0f end")
	if strings.ContainsAny(got, "\ufe0f📁✔") || !strings.HasPrefix(got, "OK ") || !strings.HasSuffix(got, " end") {
		t.Errorf("sanitize: %q", got)
	}
	for _, r := range got {
		if r != ' ' && !glyph(ui.Body, r).ok {
			t.Errorf("%q has no glyph", r)
		}
	}
	if s := sanitize(ui.Body, "a\u2028b"); s != "a\u2028b" {
		t.Errorf("hard breaks must survive: %q", s)
	}
}

func lineWidth(l Line) int {
	w := 0
	for _, r := range l.Runs {
		w = max(w, r.X+r.W)
	}
	return w
}

func TestWrap(t *testing.T) {
	const width = 380
	long := strings.Repeat("abcdefghij", 12) // wider than a line
	d := Parse("Some text with `code that has spaces` in it, and a very long word " + long + " then [a link](apps/x) to finish the paragraph off nicely.  \nAfter a hard break.\n\n- a list item that is long enough to wrap onto a second line here\n\n```\n" + long + "\n  indented\n```\n")
	pg := Layout(d, width)
	var all strings.Builder
	for i, l := range pg.Lines {
		if w := lineWidth(l); w > width {
			t.Errorf("line %d is %d px wide: %q", i, w, l.text())
		}
		for _, r := range l.Runs {
			all.WriteString(r.Text)
		}
		all.WriteString("\n")
	}
	text := all.String()
	if !strings.Contains(text, "code that has spaces") && !strings.Contains(text, "code that") {
		t.Errorf("code span split into separate runs:\n%s", text)
	}
	if strings.Count(strings.ReplaceAll(text, "\n", ""), long) != 2 {
		t.Errorf("the long word should come back whole when its pieces are joined:\n%s", text)
	}
	// the hard break starts a new line
	hb := -1
	for i, l := range pg.Lines {
		if len(l.Runs) > 0 && strings.HasPrefix(l.Runs[0].Text, "After a hard break") {
			hb = i
		}
	}
	if hb < 0 {
		t.Errorf("no line starts at the hard break:\n%s", text)
	}
	// a wrapped list item hangs under its text, not under the bullet
	for i, l := range pg.Lines {
		if len(l.Runs) > 0 && l.Runs[0].Text == "•" {
			next := pg.Lines[i+1]
			if next.Runs[0].X != l.Runs[1].X {
				t.Errorf("hanging indent: %d vs %d", next.Runs[0].X, l.Runs[1].X)
			}
		}
	}
	// code keeps its indentation and is cut by letters
	var code []Line
	for _, l := range pg.Lines {
		if len(l.Runs) > 0 && l.Runs[0].Deco&decoMono != 0 {
			code = append(code, l)
		}
	}
	if len(code) < 3 || code[len(code)-1].Runs[0].Text != "  indented" {
		t.Errorf("code lines: %d %+v", len(code), code)
	}
	// links are recorded with the line they start on
	if len(pg.Links) != 1 || pg.Links[0].URL != "apps/x" || pg.Links[0].Line < 0 || !strings.Contains(pg.Lines[pg.Links[0].Line].text(), "a link") {
		t.Errorf("links: %+v", pg.Links)
	}
	// every line is laid out exactly once: Y adds up
	y := 0
	for _, l := range pg.Lines {
		if l.Y != y {
			t.Fatalf("line at %d, expected %d", l.Y, y)
		}
		y += l.H
	}
	if pg.Height != y {
		t.Errorf("height %d vs %d", pg.Height, y)
	}
}

func TestScroll(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "Paragraph %d.\n\n", i)
	}
	pg := Layout(Parse(b.String()), 400)
	const h = 320
	mt := pg.maxTop(h)
	if pg.Height-pg.Lines[mt].Y > h || pg.Height-pg.Lines[mt-1].Y <= h {
		t.Fatalf("maxTop %d is not the last full screen", mt)
	}
	top, pages := 0, 0
	for top < mt {
		next := pg.pageDown(top, h)
		if next <= top {
			t.Fatal("page down is stuck")
		}
		// nothing skipped: the new top was not fully on the screen before
		if next < mt && pg.Lines[next].Y+pg.Lines[next].H-pg.Lines[top].Y <= h {
			t.Fatalf("page down from %d to %d skips nothing visible?", top, next)
		}
		top = next
		pages++
	}
	if top != mt || pages < 3 {
		t.Errorf("paged to %d in %d", top, pages)
	}
	for top > 0 {
		prev := pg.pageUp(top, h)
		if prev >= top || pg.Lines[top].Y-pg.Lines[prev].Y > h {
			t.Fatalf("page up from %d to %d", top, prev)
		}
		top = prev
	}
	if s := pg.scroll(0, 80, h); s == 0 || pg.Lines[s].Y < 80 {
		t.Errorf("scroll down: %d", s)
	}
	if s := pg.scroll(mt, 1000, h); s != mt {
		t.Errorf("scroll past the end: %d", s)
	}
	if l := pg.find("paragraph 42"); l < 0 || !strings.Contains(pg.Lines[l].text(), "Paragraph 42") {
		t.Errorf("find: %d", l)
	}
}

// onionEntries mimics the shape of OnionUI/Onion's website/docs.
var onionEntries = []Entry{
	{"index.md", "r"},
	{"01-features/index.md", "f"},
	{"01-features/assets/x.png", "img"},
	{"02-installation/01-fresh-install.mdx", "a"},
	{"02-installation/02-upgrading.mdx", "b"},
	{"02-installation/index.mdx", "c"},
	{"04-emulators/01-arcade/01-arcade-default.md", "d"},
	{"04-emulators/01-arcade/index.mdx", "e"},
	{"04-emulators/02-consoles/23-nintendo-nes.md", "g"},
	{"04-emulators/02-consoles/03-atari-2600.md", "h"},
	{"04-emulators/Rom folders - Quick reference.md", "i"},
	{"04-emulators/index.mdx", "j"},
	{"07-apps/01-included-in-onion/game-switcher.md", "k"},
	{"07-apps/01-included-in-onion/activity-tracker.md", "l"},
	{"07-apps/01-included-in-onion/index.mdx", "m"},
	{"08-guides/_category_.json", "n"},
	{"08-guides/03-dev/01-setup.md", "o"},
	{"08-guides/02-theme-design/index.md", "p"},
	{"08-guides/04-editing-docs.mdx", "q"},
	{"zz-extra/_category_.json", "s"},
	{"zz-extra/page.md", "t"},
}

func labels(n *Node) string {
	var out []string
	for _, k := range n.Kids {
		out = append(out, k.Label)
	}
	return strings.Join(out, ", ")
}

func TestNavOrder(t *testing.T) {
	isDocs := []Entry{}
	for _, e := range onionEntries {
		if isDoc(e.Path) {
			isDocs = append(isDocs, e)
		}
	}
	meta := map[string]Meta{
		"index.md":                                         {Title: "Introduction", Slug: "/", HasPos: true, Pos: 0},
		"04-emulators/index.mdx":                           {Title: "Emulators"},
		"08-guides/_category_.json":                        {Label: "Guides"},
		"zz-extra/_category_.json":                         {Label: "Extras first", Pos: 0.5, HasPos: true},
		"07-apps/01-included-in-onion/activity-tracker.md": {Title: "Activity Tracker", Pos: 99, HasPos: true},
		"02-installation/02-upgrading.mdx":                 {Title: "Upgrading", Label: "Upgrade"},
	}
	root, slugs := buildNav(isDocs, meta)
	if got := labels(root); got != "Introduction, Extras first, Features, Installation, Emulators, Apps, Guides" {
		t.Errorf("sections: %s", got)
	}
	inst := root.Kids[3]
	if inst.Page != "02-installation/index.mdx" || labels(inst) != "Fresh install, Upgrade" || !inst.IsList() {
		t.Errorf("installation: %q %s", inst.Page, labels(inst))
	}
	if f := root.Kids[2]; f.IsList() || f.Page != "01-features/index.md" {
		t.Errorf("a folder with only an index opens the page: %+v", f)
	}
	emu := root.Kids[4]
	if got := labels(emu); got != "Arcade, Consoles, Rom folders   Quick reference" {
		t.Errorf("emulators: %s", got)
	}
	if got := labels(emu.Kids[1]); got != "Atari 2600, Nintendo nes" {
		t.Errorf("consoles (numbers order them): %s", got)
	}
	if emu.Count() != 6 {
		t.Errorf("count %d", emu.Count())
	}
	apps := root.Kids[5].Kids[0]
	if got := labels(apps); got != "Activity Tracker, Game switcher" {
		t.Errorf("a sidebar_position puts a page before unnumbered ones: %s", got)
	}
	if slugs["/"] != "index.md" || slugs["/emulators/consoles/nintendo-nes"] != "04-emulators/02-consoles/23-nintendo-nes.md" || slugs["/emulators"] != "04-emulators/index.mdx" {
		t.Errorf("slugs: %v", slugs)
	}
}

func TestResolve(t *testing.T) {
	meta := map[string]Meta{
		"01-features/index.md":                             {Slug: "/features"},
		"07-apps/01-included-in-onion/game-switcher.md":    {Slug: "/apps/game-switcher"},
		"07-apps/01-included-in-onion/activity-tracker.md": {Slug: "/apps/activity-tracker"},
		"04-emulators/index.mdx":                           {Slug: "/emulators"},
	}
	var entries []Entry
	for _, e := range onionEntries {
		if isDoc(e.Path) {
			entries = append(entries, e)
		}
	}
	_, slugs := buildNav(entries, meta)
	from := "01-features/index.md"
	for _, tc := range []struct{ href, page, anchor string }{
		{"apps/game-switcher", "07-apps/01-included-in-onion/game-switcher.md", ""},
		{"./apps/activity-tracker#usage", "07-apps/01-included-in-onion/activity-tracker.md", "usage"},
		{"/docs/emulators", "04-emulators/index.mdx", ""},
		{"https://onionui.github.io/docs/apps/game-switcher", "07-apps/01-included-in-onion/game-switcher.md", ""},
		{"#shortcuts", from, "shortcuts"},
		{"../02-installation/01-fresh-install.mdx", "02-installation/01-fresh-install.mdx", ""},
		{"/" + escapePath("04-emulators/Rom folders - Quick reference.md"), "04-emulators/Rom folders - Quick reference.md", ""},
		{"installation#upgrading", "02-installation/index.mdx", "upgrading"}, // by folder name
		{"atari-2600", "04-emulators/02-consoles/03-atari-2600.md", ""},      // by file name
		{"https://github.com/OnionUI", "", ""},
		{"nowhere-at-all", "", ""},
	} {
		p, a, ok := resolve(from, pageSlug(from, meta[from].Slug), tc.href, slugs, entries)
		if p != tc.page || a != tc.anchor || ok != (tc.page != "") {
			t.Errorf("%q: got %q #%q %v", tc.href, p, a, ok)
		}
	}
	// the root page's links are relative to /docs/
	if p, _, ok := resolve("index.md", "/", "./docs/features", slugs, entries); !ok || p != "01-features/index.md" {
		t.Errorf("from the root: %q", p)
	}
}

// TestDocsOffline downloads everything from a fake GitHub, then reads it
// all back with the server gone.
func TestDocsOffline(t *testing.T) {
	files := map[string]string{
		"index.md":                  "---\nslug: /\nsidebar_position: 0\n---\n# Introduction\n\nHello [faq](faq).\n",
		"03-faq/index.mdx":          "---\nslug: /faq\n---\nimport X from 'y';\n\n# FAQ\n\n:::tip\nA tip with the word banana.\n:::\n",
		"08-guides/_category_.json": `{"label": "Guides", "position": 8}`,
		"08-guides/01-scraping.md":  "# Scraping\n\nText.\n",
	}
	type ent struct{ Path, Type, SHA string }
	var tree []ent
	for p := range files {
		tree = append(tree, ent{p, "blob", "sha-" + p})
	}
	tree = append(tree, ent{"03-faq/assets/q.png", "blob", "z"}, ent{"03-faq", "tree", "t"})
	treeHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tree" {
			treeHits++
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			json.NewEncoder(w).Encode(map[string]any{"tree": tree})
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/raw/")
		if b, ok := files[p]; ok {
			w.Write([]byte(b))
			return
		}
		http.NotFound(w, r)
	}))
	oldTree, oldRaw := treeURL, rawURL
	treeURL, rawURL = srv.URL+"/tree", srv.URL+"/raw/"
	defer func() { treeURL, rawURL = oldTree, oldRaw }()

	dir := t.TempDir()
	client := &http.Client{Timeout: 5 * time.Second}
	d := NewDocs(dir, client)
	ctx := context.Background()
	if err := d.RefreshTree(ctx); err != nil {
		t.Fatal(err)
	}
	if got := labels(d.Nav()); got != "Introduction, Faq, Guides" {
		t.Errorf("nav before pages are read: %s", got)
	}
	failed, err := d.DownloadAll(ctx, func(int, int) {})
	if err != nil || failed != 0 {
		t.Fatal(failed, err)
	}
	if got := labels(d.Nav()); got != "Introduction, FAQ, Guides" {
		t.Errorf("nav after: %s", got)
	}
	if saved, total := d.Saved(); saved != 3 || total != 3 || len(d.Missing()) != 0 {
		t.Errorf("saved %d/%d, missing %v", saved, total, d.Missing())
	}
	if err := d.RefreshTree(ctx); err != nil || treeHits != 3 { // DownloadAll checked too
		t.Errorf("refresh with ETag: %v, %d requests", err, treeHits)
	}
	if p, _, ok := d.Resolve("index.md", "faq"); !ok || p != "03-faq/index.mdx" {
		t.Errorf("resolve: %q", p)
	}
	if hits := d.Search(ctx, "banana"); len(hits) != 1 || hits[0].Path != "03-faq/index.mdx" || !strings.Contains(hits[0].Snippet, "banana") {
		t.Errorf("search: %+v", hits)
	}
	d.SaveMeta()
	srv.Close()

	// a new start, with no network
	d2 := NewDocs(dir, client)
	err = d2.RefreshTree(ctx)
	if err == nil || !d2.Offline() {
		t.Fatalf("expected offline: %v", err)
	}
	if got := labels(d2.Nav()); got != "Introduction, FAQ, Guides" {
		t.Errorf("offline nav: %s", got)
	}
	body, err := d2.Get(ctx, "03-faq/index.mdx")
	if err != nil || !strings.Contains(body, "banana") {
		t.Errorf("offline read: %v %q", err, body)
	}
	if _, err := d2.Get(ctx, "nope.md"); err == nil {
		t.Error("a page never saved can't be read offline")
	}
}

func TestPrettyNames(t *testing.T) {
	for in, want := range map[string]string{"03-faq.md": "Faq", "émulateurs": "Émulateurs", "01-": "01-", "my_page": "My page"} {
		if got := pretty(in); got != want {
			t.Errorf("pretty(%q) = %q, want %q", in, got, want)
		}
	}
	// a damaged file list must not send buildNav round in circles
	nav, _ := buildNav([]Entry{{Path: "/a.md"}, {Path: "/x/b.md"}, {Path: "../c.md"}}, nil)
	if nav.Count() != 3 {
		t.Errorf("odd paths: %d pages, want 3", nav.Count())
	}
}
