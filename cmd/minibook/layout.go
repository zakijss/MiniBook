package main

import (
	"image/color"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/zakijss/minideck/ui"
)

// A page is laid out once, when it is opened: every line is wrapped and
// placed, so scrolling only draws the lines in view.

const (
	decoCode uint8 = 1 << iota // a `code` pill behind the text
	decoLink                   // an in-doc link: coloured and underlined
	decoMono                   // one letter per monoCell px (code blocks)
)

// Code blocks are drawn with a fixed advance per letter, so ASCII tables
// and diagrams line up (there is no monospaced font).
const monoCell = 12

var (
	linkColor = color.RGBA{0x64, 0xd2, 0xff, 0xff}
	// callout colours, by kind; the rest are Muted (see noteColor)
	noteColors = map[string]color.RGBA{
		"tip":       ui.Good,
		"success":   ui.Good,
		"info":      linkColor,
		"important": {0xd0, 0x8a, 0xff, 0xff},
		"caution":   ui.Warn,
		"warning":   ui.Warn,
		"danger":    {0xff, 0x80, 0x78, 0xff}, // Bad, lighter: on a tinted card
	}
)

// noteColor is the colour of a callout of the given kind. Kinds without a
// colour of their own (note, secondary) take the theme's Muted, read on
// every call so they follow a change of theme.
func noteColor(kind string) color.RGBA {
	if c, ok := noteColors[kind]; ok {
		return c
	}
	return ui.Muted
}

// Run is text drawn at X on its line.
type Run struct {
	X, W  int
	Text  string
	Face  font.Face
	Color color.RGBA
	Deco  uint8
	Link  int // index into Page.Links, or -1
}

// Line is one row of the page, H pixels tall, Y from the page's top.
type Line struct {
	Y, H int
	Runs []Run
	Rule [2]int // a hairline across the middle, from x Rule[0] to Rule[1]
}

// Box is a background behind lines From..To: a callout, code or a quote.
type Box struct {
	Kind     string
	X0, X1   int
	From, To int
}

// PageLink is a link on a page that leads into the docs, and the line it
// starts on.
type PageLink struct {
	URL, Text string
	Line      int
}

// Page is a laid-out page. X positions are from the text area's left.
type Page struct {
	Path, Title string
	Lines       []Line
	Boxes       []Box
	Links       []PageLink
	Anchors     map[string]int // heading id -> line
	Height      int
}

// ---- measuring ----

type glyphInfo struct {
	adv fixed.Int26_6
	ok  bool
}

// glyphs caches advances per face, so wrapping a long page doesn't go back
// to the font for every letter. Only used from the UI goroutine.
var glyphs = map[font.Face]map[rune]glyphInfo{}

func glyph(f font.Face, r rune) glyphInfo {
	m := glyphs[f]
	if m == nil {
		m = map[rune]glyphInfo{}
		glyphs[f] = m
	}
	g, hit := m[r]
	if !hit {
		g.adv, g.ok = f.GlyphAdvance(r)
		m[r] = g
	}
	return g
}

// measure is the width of s in f (kerning left out: it only narrows).
func measure(f font.Face, s string) int {
	var w fixed.Int26_6
	for _, r := range s {
		w += glyph(f, r).adv
	}
	return w.Ceil()
}

// Characters the interface fonts lack (emoji, mostly), and what to show
// instead.
var substitutes = map[rune]string{
	'✔': "✓", '✅': "✓", '☑': "✓", '✓': "✓",
	'❌': "✗", '✖': "✗", '✗': "✗", '❎': "✗",
	'⚠': "!", '❗': "!", '❕': "!",
	'❤': "♥", '♥': "♥", '💖': "♥",
	'➡': "→", '⬅': "←", '⬆': "↑", '⬇': "↓", '👉': "→",
	'⭐': "*", '🌟': "*",
}

// sanitize replaces or drops characters the face can't draw.
func sanitize(f font.Face, s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf || s[i] == '\t' {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\t':
			b.WriteString("    ")
			continue
		case 0xfe0e, 0xfe0f, 0x200b, 0x200d, 0x00ad:
			continue
		case 0x00a0:
			b.WriteByte(' ')
			continue
		}
		if r == hardBreakRune || glyph(f, r).ok {
			b.WriteRune(r)
			continue
		}
		if sub, ok := substitutes[r]; ok {
			good := true
			for _, sr := range sub {
				good = good && glyph(f, sr).ok
			}
			if !good {
				sub = map[string]string{"✓": "OK", "✗": "x", "♥": "<3", "→": "->", "←": "<-", "↑": "^", "↓": "v"}[sub]
			}
			b.WriteString(sub)
		}
		// anything else (pictographs) is dropped
	}
	return b.String()
}

// ---- wrapping ----

// piece is a styled bit of one word.
type piece struct {
	text  string
	face  font.Face
	color color.RGBA
	deco  uint8
	link  int
	space bool // a space comes before it
	brk   bool // a forced line break (no text)
	w     int
}

type style struct {
	face, bold font.Face
	color      color.RGBA
}

// layouter builds a Page.
type layouter struct {
	pg *Page
	y  int
}

func (lo *layouter) add(l Line) int {
	l.Y = lo.y
	lo.y += l.H
	lo.pg.Lines = append(lo.pg.Lines, l)
	return len(lo.pg.Lines) - 1
}

func (lo *layouter) gap(h int) { lo.add(Line{H: h}) }

// internal reports whether a link points into the docs (and so can be
// followed), judging by its form.
func internal(u string) bool {
	switch {
	case u == "":
		return false
	case strings.HasPrefix(u, "http://"), strings.HasPrefix(u, "https://"):
		return strings.Contains(u, "onionui.github.io") && strings.Contains(u, "/docs")
	case strings.Contains(u, ":"): // mailto:, etc.
		return false
	}
	ext := strings.ToLower(u)
	if i := strings.IndexAny(ext, "#?"); i >= 0 {
		ext = ext[:i]
	}
	for _, e := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".zip", ".pdf", ".mp4", ".7z"} {
		if strings.HasSuffix(ext, e) {
			return false
		}
	}
	return true
}

// pieces splits spans into words (pieces of words, when a word changes
// style midway) for wrapping.
func (lo *layouter) pieces(spans []Span, st style) []piece {
	var out []piece
	space := false
	for _, sp := range spans {
		f, col, deco, link := st.face, st.color, uint8(0), -1
		if sp.Style&Bold != 0 {
			f = st.bold
		}
		if sp.Style&Mono != 0 {
			deco |= decoCode
		}
		if sp.Style&Faded != 0 {
			col = ui.Faint
		}
		if sp.Link != "" && internal(sp.Link) {
			deco |= decoLink
			col = linkColor
			lo.pg.Links = append(lo.pg.Links, PageLink{URL: sp.Link, Text: strings.TrimSpace(sp.Text), Line: -1})
			link = len(lo.pg.Links) - 1
		}
		text := sanitize(f, sp.Text)
		word := strings.Builder{}
		emit := func() {
			if word.Len() > 0 {
				out = append(out, piece{text: word.String(), face: f, color: col, deco: deco, link: link, space: space})
				word.Reset()
				space = false
			}
		}
		for _, r := range text {
			switch r {
			case ' ', '\n', '\r':
				emit()
				space = true
			case hardBreakRune:
				emit()
				out = append(out, piece{brk: true})
				space = false
			default:
				word.WriteRune(r)
			}
		}
		emit()
	}
	for i := range out {
		if !out[i].brk {
			out[i].w = measure(out[i].face, out[i].text)
		}
	}
	return out
}

// wrap places pieces in lines no wider than width. A word wider than a
// line is broken between letters.
func wrap(ps []piece, width int) [][]Run {
	var lines [][]Run
	var cur []Run
	x := 0
	newline := func() {
		lines = append(lines, cur)
		cur, x = nil, 0
	}
	put := func(p piece, gap int) {
		px := x + gap
		if n := len(cur); n > 0 {
			last := &cur[n-1]
			if last.Face == p.face && last.Color == p.color && last.Deco == p.deco && last.Link == p.link && last.X+last.W == x {
				if gap > 0 {
					last.Text += " "
				}
				last.Text += p.text
				last.W = px + p.w - last.X
				x = px + p.w
				return
			}
		}
		cur = append(cur, Run{X: px, W: p.w, Text: p.text, Face: p.face, Color: p.color, Deco: p.deco, Link: p.link})
		x = px + p.w
	}
	for i := 0; i < len(ps); {
		if ps[i].brk {
			newline()
			i++
			continue
		}
		// a word: this piece and the ones glued to it
		j := i + 1
		ww := ps[i].w
		for j < len(ps) && !ps[j].space && !ps[j].brk {
			ww += ps[j].w
			j++
		}
		gap := 0
		if ps[i].space && len(cur) > 0 {
			gap = measure(ps[i].face, " ")
		}
		if len(cur) > 0 && x+gap+ww > width {
			newline()
			gap = 0
		}
		if ww > width { // break it up
			for k := i; k < j; k++ {
				p := ps[k]
				for p.text != "" {
					room := width - x - gap
					n, w := fitRunes(p.face, p.text, room)
					if n == 0 {
						if len(cur) == 0 { // not even one letter: take it anyway
							_, size := utf8.DecodeRuneInString(p.text)
							n, w = size, measure(p.face, p.text[:size])
						} else {
							newline()
							gap = 0
							continue
						}
					}
					q := p
					q.text, q.w = p.text[:n], w
					put(q, gap)
					gap = 0
					p.text = p.text[n:]
					if p.text != "" {
						newline()
					}
				}
			}
			i = j
			continue
		}
		for k := i; k < j; k++ {
			if k == i {
				put(ps[k], gap)
			} else {
				put(ps[k], 0)
			}
		}
		i = j
	}
	if len(cur) > 0 || len(lines) == 0 {
		lines = append(lines, cur)
	}
	return lines
}

// fitRunes returns how many bytes of s fit in w pixels, and their width.
func fitRunes(f font.Face, s string, w int) (int, int) {
	var x fixed.Int26_6
	limit := fixed.I(w)
	for i, r := range s {
		a := glyph(f, r).adv
		if x+a > limit {
			return i, x.Ceil()
		}
		x += a
	}
	return len(s), x.Ceil()
}

func lineH(f font.Face) int { return ui.LineHeight(f) + 3 }

// text lays out inline Markdown from x0 to x1 and returns its first line.
func (lo *layouter) text(spans []Span, st style, x0, x1 int) int {
	first := -1
	for _, runs := range wrap(lo.pieces(spans, st), x1-x0) {
		for k := range runs {
			runs[k].X += x0
		}
		n := lo.add(Line{H: lineH(st.face), Runs: runs})
		for _, r := range runs {
			if r.Link >= 0 && lo.pg.Links[r.Link].Line < 0 {
				lo.pg.Links[r.Link].Line = n
			}
		}
		if first < 0 {
			first = n
		}
	}
	return first
}

// ---- blocks ----

func bodyStyle() style             { return style{ui.Body, ui.Heading, ui.Text} }
func smallStyle() style            { return style{ui.Small, ui.Small, ui.Text} }
func headStyle(c color.RGBA) style { return style{ui.Heading, ui.Heading, c} }

const indentStep = 24

// Layout lays out a parsed page for a text column width pixels wide.
func Layout(d Doc, width int) *Page {
	pg := &Page{Title: d.Title, Anchors: map[string]int{}}
	lo := &layouter{pg: pg}
	lo.blocks(d.Blocks, 0, width)
	if len(pg.Lines) == 0 {
		lo.text([]Span{{Text: "This page is empty.", Style: Faded}}, bodyStyle(), 0, width)
	}
	pg.Height = lo.y
	return pg
}

func (lo *layouter) blocks(bs []Block, x0, x1 int) {
	for i, b := range bs {
		if i > 0 {
			switch {
			case b.Kind == Heading && b.Level <= 2:
				lo.gap(14)
			case b.Kind == Heading:
				lo.gap(16)
			case b.Kind == Item && bs[i-1].Kind == Item:
				lo.gap(4)
			default:
				lo.gap(12)
			}
		}
		lo.block(b, x0, x1)
	}
}

func nested(b Block, x0 int) int {
	if b.Level >= 0 {
		return x0 + (b.Level+1)*indentStep
	}
	return x0
}

func (lo *layouter) block(b Block, x0, x1 int) {
	switch b.Kind {
	case Para:
		lo.text(Inline(b.Text), bodyStyle(), x0, x1)
	case Heading:
		st := headStyle(ui.Text)
		switch {
		case b.Level == 1:
			st = style{ui.Title, ui.Title, ui.Text}
		case b.Level == 2 && lo.y > 0:
			lo.add(Line{H: 12, Rule: [2]int{x0, x1}})
		case b.Level >= 4:
			st.color = ui.Muted
		}
		n := lo.text(Inline(b.Text), st, x0, x1)
		if b.Anchor != "" {
			if _, dup := lo.pg.Anchors[b.Anchor]; !dup {
				lo.pg.Anchors[b.Anchor] = max(0, n-1)
			}
		}
	case Item:
		ix := x0 + b.Level*indentStep
		tx := ix + indentStep
		if b.Marker != "" {
			mw := measure(ui.Body, b.Marker)
			if mw+8 > indentStep {
				tx = ix + mw + 8
			}
			n := lo.text(Inline(b.Text), bodyStyle(), tx, x1)
			col := ui.Muted
			lo.pg.Lines[n].Runs = append([]Run{{X: ix, W: mw, Text: b.Marker, Face: ui.Body, Color: col, Link: -1}}, lo.pg.Lines[n].Runs...)
		} else {
			lo.text(Inline(b.Text), bodyStyle(), tx, x1)
		}
	case Code:
		x0 = nested(b, x0)
		from := lo.add(Line{H: 8})
		cols := max(8, (x1-x0-28)/monoCell)
		for _, l := range b.Lines {
			rs := []rune(sanitize(ui.Small, strings.TrimRight(l, " ")))
			for {
				n := min(len(rs), cols)
				lo.add(Line{H: lineH(ui.Small), Runs: []Run{{X: x0 + 14, W: n * monoCell, Text: string(rs[:n]), Face: ui.Small, Color: ui.Text, Deco: decoMono, Link: -1}}})
				rs = rs[n:]
				if len(rs) == 0 {
					break
				}
			}
		}
		to := lo.add(Line{H: 8})
		lo.pg.Boxes = append(lo.pg.Boxes, Box{Kind: "code", X0: x0, X1: x1, From: from, To: to})
	case Callout:
		x0 = nested(b, x0)
		col := noteColor(b.Note)
		from := lo.add(Line{H: 10})
		title := b.Text
		if title == "" {
			title = strings.ToUpper(b.Note[:1]) + b.Note[1:]
		}
		lo.text(Inline(title), headStyle(col), x0+26, x1-16)
		if len(b.Kids) > 0 {
			lo.gap(4)
			lo.blocks(b.Kids, x0+26, x1-16)
		}
		to := lo.add(Line{H: 12})
		lo.pg.Boxes = append(lo.pg.Boxes, Box{Kind: b.Note, X0: x0, X1: x1, From: from, To: to})
	case Quote:
		x0 = nested(b, x0)
		from := len(lo.pg.Lines)
		lo.blocks(b.Kids, x0+20, x1)
		if to := len(lo.pg.Lines) - 1; to >= from {
			lo.pg.Boxes = append(lo.pg.Boxes, Box{Kind: "quote", X0: x0, X1: x1, From: from, To: to})
		}
	case Rule:
		lo.add(Line{H: 16, Rule: [2]int{x0, x1}})
	case Table:
		lo.table(b, x0, x1)
	}
}

// table lays out a table: columns get their natural width when they fit,
// else a fair share (narrow columns keep theirs), and cells wrap.
func (lo *layouter) table(b Block, x0, x1 int) {
	const pad = 14
	cols := 0
	for _, r := range b.Rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return
	}
	cells := make([][][]Span, len(b.Rows))
	natural := make([]int, cols)
	for i, r := range b.Rows {
		cells[i] = make([][]Span, cols)
		for c := 0; c < cols; c++ {
			if c < len(r) {
				cells[i][c] = Inline(r[c])
			}
			w := 0
			for _, sp := range cells[i][c] {
				w += measure(ui.Small, sanitize(ui.Small, sp.Text))
			}
			natural[c] = max(natural[c], w+pad)
		}
	}
	widths := make([]int, cols)
	avail := x1 - x0
	total := 0
	for _, w := range natural {
		total += w
	}
	if total <= avail {
		copy(widths, natural)
	} else {
		// max-min fair shares
		left := avail
		done := make([]bool, cols)
		for n := cols; n > 0; n-- {
			share := left / n
			best := -1
			for c := 0; c < cols; c++ {
				if !done[c] && (best < 0 || natural[c] < natural[best]) {
					best = c
				}
			}
			widths[best] = max(48, min(natural[best], share))
			if natural[best] > share {
				// every remaining column is at least this wide: share evenly
				for c := 0; c < cols; c++ {
					if !done[c] {
						widths[c] = max(48, share)
						done[c] = true
					}
				}
				break
			}
			left -= widths[best]
			done[best] = true
		}
	}
	tableW := 0
	for _, w := range widths {
		tableW += w
	}
	for i := range b.Rows {
		if i > 0 {
			lo.add(Line{H: 9, Rule: [2]int{x0, x0 + min(tableW, avail)}})
		}
		st := smallStyle()
		if i < len(b.Head) && b.Head[i] {
			st.color = ui.Muted
		}
		var colLines [][][]Run
		rows := 1
		for c := 0; c < cols; c++ {
			ls := wrap(lo.pieces(cells[i][c], st), max(8, widths[c]-pad))
			colLines = append(colLines, ls)
			rows = max(rows, len(ls))
		}
		for k := 0; k < rows; k++ {
			var runs []Run
			x := x0
			for c := 0; c < cols; c++ {
				if k < len(colLines[c]) {
					for _, r := range colLines[c][k] {
						r.X += x
						runs = append(runs, r)
					}
				}
				x += widths[c]
			}
			n := lo.add(Line{H: lineH(ui.Small), Runs: runs})
			for _, r := range runs {
				if r.Link >= 0 && lo.pg.Links[r.Link].Line < 0 {
					lo.pg.Links[r.Link].Line = n
				}
			}
		}
	}
}

// ---- scrolling ----

// fits is how many lines from top fit in h pixels (at least one).
func (pg *Page) fits(top, h int) int {
	if len(pg.Lines) == 0 {
		return 1
	}
	top = min(max(top, 0), len(pg.Lines)-1)
	n := 0
	for i := top; i < len(pg.Lines); i++ {
		if pg.Lines[i].Y+pg.Lines[i].H-pg.Lines[top].Y > h {
			break
		}
		n++
	}
	return max(1, n)
}

// maxTop is the last top line that still fills the view.
func (pg *Page) maxTop(h int) int {
	if len(pg.Lines) == 0 {
		return 0
	}
	for i := 0; i < len(pg.Lines); i++ {
		if pg.Height-pg.Lines[i].Y <= h {
			return i
		}
	}
	return len(pg.Lines) - 1
}

// scroll moves top by about px pixels (negative: up), by whole lines.
func (pg *Page) scroll(top, px, h int) int {
	if len(pg.Lines) == 0 {
		return 0
	}
	top = min(max(top, 0), len(pg.Lines)-1)
	y := pg.Lines[top].Y
	if px > 0 {
		for top < pg.maxTop(h) && pg.Lines[top].Y-y < px {
			top++
		}
	} else {
		for top > 0 && y-pg.Lines[top].Y < -px {
			top--
		}
	}
	return top
}

// pageDown and pageUp move by a screenful.
func (pg *Page) pageDown(top, h int) int {
	return min(pg.maxTop(h), max(top+1, top+pg.fits(top, h)))
}

func (pg *Page) pageUp(top, h int) int {
	if top <= 0 {
		return 0
	}
	y := pg.Lines[top].Y
	i := top
	for i > 0 && y-pg.Lines[i-1].Y <= h {
		i--
	}
	return min(i, top-1)
}

// lineText is the text on a line, for finding a search hit.
func (l Line) text() string {
	var b strings.Builder
	for _, r := range l.Runs {
		b.WriteString(r.Text)
		b.WriteByte(' ')
	}
	return b.String()
}

// find returns the first line containing q (any case), or -1.
func (pg *Page) find(q string) int {
	q = strings.ToLower(q)
	for i, l := range pg.Lines {
		if strings.Contains(strings.ToLower(l.text()), q) {
			return i
		}
	}
	return -1
}
