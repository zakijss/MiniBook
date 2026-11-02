package main

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// The docs are Docusaurus Markdown and MDX. This file turns a page into a
// short list of blocks (headings, paragraphs, list items, code, tables,
// callouts) with the MDX and HTML taken out, ready for layout.go to wrap.

// Front is a page's front matter.
type Front struct {
	Title, Label, Slug, Desc string
	Pos                      float64
	HasPos                   bool
}

// splitFront takes the front matter off the top of a page.
func splitFront(src string) (Front, string) {
	var f Front
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.TrimPrefix(src, "\ufeff")
	if !strings.HasPrefix(src, "---\n") {
		return f, src
	}
	end := strings.Index(src[4:], "\n---")
	if end < 0 {
		return f, src
	}
	head := src[4 : 4+end]
	rest := src[4+end+4:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	} else {
		rest = ""
	}
	for _, l := range strings.Split(head, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		switch strings.TrimSpace(k) {
		case "title":
			f.Title = v
		case "sidebar_label":
			f.Label = v
		case "slug":
			f.Slug = v
		case "description":
			f.Desc = v
		case "sidebar_position":
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				f.Pos, f.HasPos = n, true
			}
		}
	}
	return f, rest
}

// BlockKind says what a Block is.
type BlockKind int

// The kinds of Block.
const (
	Para BlockKind = iota
	Heading
	Item // a list item, or a paragraph continuing one (Marker "")
	Code
	Table
	Callout
	Quote
	Rule
	Cards // <DocCardList/>: the category's pages, filled in by the app
)

// Block is one block of a page, ready to lay out.
type Block struct {
	Kind   BlockKind
	Level  int    // heading level (1-6); list nesting (0-)
	Marker string // list marker: "•" or "1."
	Text   string // inline Markdown (Para, Heading, Item); the callout's title
	Anchor string // heading id
	Lines  []string
	Rows   [][]string // Table
	Head   []bool     // Table: which rows are header rows
	Note   string     // Callout kind: note, tip, info, caution, danger...
	Kids   []Block    // Callout, Quote
}

// Doc is a parsed page.
type Doc struct {
	Front  Front
	Title  string // front matter title, or the first # heading
	Blocks []Block
}

// Parse parses a page.
func Parse(src string) Doc {
	f, body := splitFront(src)
	body = strings.NewReplacer("{frontMatter.description}", f.Desc, "{frontMatter.title}", f.Title).Replace(body)
	d := Doc{Front: f, Blocks: parseBlocks(prepare(body))}
	d.Title = f.Title
	if len(d.Blocks) > 0 && d.Blocks[0].Kind == Heading && d.Blocks[0].Level == 1 {
		if d.Title == "" {
			d.Title = plain(d.Blocks[0].Text)
		}
		d.Blocks = d.Blocks[1:] // the reader shows the title in its header
	}
	return d
}

// PageTitle finds a page's title without parsing all of it: front matter
// title, else the first # heading.
func PageTitle(src string) (Front, string) {
	f, body := splitFront(src)
	if f.Title != "" {
		return f, f.Title
	}
	fence := ""
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(l)
		if fence != "" {
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
			continue
		}
		if f := fenceOf(t); f != "" {
			fence = f
			continue
		}
		if strings.HasPrefix(t, "# ") {
			h, _ := headingID(convertHTML(t[2:]))
			return f, plain(h)
		}
	}
	return f, ""
}

// ---- preparation: comments, imports, tags over several lines ----

var reOpenTag = regexp.MustCompile(`<[A-Za-z][^<>]*$`)

func fenceOf(t string) string {
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(t, f) {
			n := len(t) - len(strings.TrimLeft(t, f[:1]))
			return strings.Repeat(f[:1], n)
		}
	}
	return ""
}

// prepare splits the body into lines and drops what is never shown: MDX
// imports and exports, HTML and MDX comments. A tag split over several
// lines (<img\n src=...\n />) is joined into one line.
func prepare(body string) []string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var out []string
	fence := ""
	comment := "" // the end marker of an open comment
	exportDepth := 0
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		if fence != "" {
			out = append(out, l)
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if exportDepth > 0 {
			exportDepth += strings.Count(l, "{") - strings.Count(l, "}")
			continue
		}
		if comment != "" {
			j := strings.Index(l, comment)
			if j < 0 {
				continue
			}
			l = l[j+len(comment):]
			comment = ""
			t = strings.TrimSpace(l)
		}
		if f := fenceOf(t); f != "" {
			fence = f
			out = append(out, l)
			continue
		}
		if strings.HasPrefix(t, "import ") && (strings.Contains(t, " from ") || strings.HasPrefix(t, "import '") || strings.HasPrefix(t, `import "`)) {
			continue
		}
		if strings.HasPrefix(t, "export ") {
			exportDepth = strings.Count(l, "{") - strings.Count(l, "}")
			continue
		}
		// comments: <!-- --> and {/* */}, perhaps over several lines
		for _, c := range [][2]string{{"<!--", "-->"}, {"{/*", "*/}"}} {
			for {
				j := strings.Index(l, c[0])
				if j < 0 {
					break
				}
				k := strings.Index(l[j:], c[1])
				if k < 0 {
					l = l[:j]
					comment = c[1]
					break
				}
				l = l[:j] + l[j+k+len(c[1]):]
			}
		}
		for n := 0; n < 30 && i+1 < len(lines) && reOpenTag.MatchString(stripCode(l)); n++ {
			i++
			l += " " + strings.TrimSpace(lines[i])
		}
		out = append(out, l)
	}
	return out
}

// stripCode blanks out `code spans`, so tags inside them aren't seen.
func stripCode(l string) string {
	if !strings.Contains(l, "`") {
		return l
	}
	parts := splitCode(l)
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 0 {
			b.WriteString(p)
		}
	}
	return b.String()
}

// splitCode splits a line into text and `code` parts, alternating (even
// indexes are text, odd ones code with their backticks).
func splitCode(l string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(l); {
		if l[i] != '`' {
			i++
			continue
		}
		n := 1
		for i+n < len(l) && l[i+n] == '`' {
			n++
		}
		tick := l[i : i+n]
		j := i + n
		end := -1
		for j < len(l) {
			k := strings.Index(l[j:], tick)
			if k < 0 {
				break
			}
			k += j
			m := k + n
			if m < len(l) && l[m] == '`' { // a longer run: not ours
				for m < len(l) && l[m] == '`' {
					m++
				}
				j = m
				continue
			}
			end = k
			break
		}
		if end < 0 {
			i += n
			continue
		}
		parts = append(parts, l[start:i], l[i:end+n])
		start = end + n
		i = start
	}
	return append(parts, l[start:])
}

// ---- HTML and JSX ----

// A forced line break inside a paragraph.
const (
	hardBreak     = "\u2028"
	hardBreakRune = '\u2028'
)

var (
	reTabItem  = regexp.MustCompile(`<TabItem\b[^>]*?\blabel=["']([^"']*)["'][^>]*>`)
	reIconLink = regexp.MustCompile(`(?s)<IconLink\b[^>]*?\bhref=["']([^"']*)["'][^>]*>(.*?)</IconLink>`)
	reAnchor   = regexp.MustCompile(`(?is)<a\b[^>]*?\bhref=["']([^"']*)["'][^>]*>(.*?)</a>`)
	reKbd      = regexp.MustCompile(`(?is)<kbd>(.*?)</kbd>`)
	reImgAlt   = regexp.MustCompile(`(?i)<img\b[^>]*?\balt=["']([^"']+)["'][^>]*>`)
	reBr       = regexp.MustCompile(`(?i)<br\s*/?>`)
	reBold     = regexp.MustCompile(`(?i)</?(b|strong)>`)
	// Known HTML tags, and any JSX component (capitalised), with attributes
	// that may hold "strings" and {expressions}.
	reTag    = regexp.MustCompile(`</?(?:[A-Z][A-Za-z0-9.]*|a|abbr|b|big|br|center|code|del|details|div|em|figcaption|figure|font|h[1-6]|hr|i|iframe|img|ins|kbd|li|mark|ol|p|picture|pre|s|section|small|source|span|strong|sub|summary|sup|table|tbody|td|tfoot|th|thead|tr|u|ul|video)\b(?:[^<>{}"']|"[^"]*"|'[^']*'|\{(?:[^{}]|\{[^{}]*\})*\})*/?>`)
	reTagged = regexp.MustCompile(`<[A-Za-z/]`)
	// an MDX {expression} in text: {props.x}, {' '}
	reExpr  = regexp.MustCompile(`\{\s*(?:[A-Za-z_$][\w$.]*|'[^']*'|"[^"]*")\s*\}`)
	reCards = regexp.MustCompile(`^\s*<DocCardList\b[^>]*/>\s*$`)
)

// convertHTML turns the inline HTML and JSX of one line into Markdown (or
// removes it), leaving `code spans` alone.
func convertHTML(l string) string {
	if strings.Contains(l, "{") {
		parts := splitCode(l)
		for i := 0; i < len(parts); i += 2 {
			parts[i] = reExpr.ReplaceAllString(parts[i], "")
		}
		l = strings.Join(parts, "")
	}
	if !reTagged.MatchString(l) {
		return l
	}
	if m := reTabItem.FindStringSubmatch(l); m != nil {
		// a tab of a <Tabs> group becomes a small heading
		return "#### " + m[1]
	}
	parts := splitCode(l)
	for i := 0; i < len(parts); i += 2 {
		p := parts[i]
		if !reTagged.MatchString(p) {
			continue
		}
		p = reIconLink.ReplaceAllString(p, "[$2]($1)")
		p = reAnchor.ReplaceAllString(p, "[$2]($1)")
		p = reKbd.ReplaceAllString(p, "`$1`")
		p = reImgAlt.ReplaceAllString(p, "![$1]()")
		p = reBr.ReplaceAllString(p, hardBreak)
		p = reBold.ReplaceAllString(p, "**")
		p = reTag.ReplaceAllString(p, "")
		parts[i] = p
	}
	return strings.Join(parts, "")
}

var (
	reRow  = regexp.MustCompile(`(?is)<tr\b[^>]*>(.*?)</tr>`)
	reCell = regexp.MustCompile(`(?is)<(td|th)\b[^>]*>(.*?)</(?:td|th)>`)
)

// htmlTable reads the rows of an HTML <table>. Rows with nothing but
// pictures in them (Onion's icon grids) come out empty and are dropped.
func htmlTable(s string) (rows [][]string, head []bool) {
	for _, m := range reRow.FindAllStringSubmatch(s, -1) {
		var row []string
		th := false
		any := false
		for _, c := range reCell.FindAllStringSubmatch(m[1], -1) {
			txt := strings.TrimSpace(convertHTML(strings.Join(strings.Fields(c[2]), " ")))
			if strings.EqualFold(c[1], "th") {
				th = true
			}
			if plain(txt) != "" {
				any = true
			}
			row = append(row, txt)
		}
		if any {
			rows = append(rows, row)
			head = append(head, th)
		}
	}
	return
}

// ---- blocks ----

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	reItem    = regexp.MustCompile(`^(\s*)([-*+]|\d{1,3}[.)])\s+(.*)$`)
	reEmptyLi = regexp.MustCompile(`^(\s*)([-*+]|\d{1,3}[.)])\s*$`)
	reHR      = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
	reTableSp = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$`)
	reAdmon   = regexp.MustCompile(`^:::+\s*([A-Za-z]+)(?:\[([^\]]*)\])?\s*(.*)$`)
	reID      = regexp.MustCompile(`\s*\{#([^}]+)\}\s*$`)
)

func headingID(t string) (string, string) {
	if m := reID.FindStringSubmatchIndex(t); m != nil {
		return t[:m[0]], t[m[2]:m[3]]
	}
	return t, ""
}

func isTableStart(lines []string, i int) bool {
	t := strings.TrimSpace(lines[i])
	return strings.Contains(t, "|") && i+1 < len(lines) && reTableSp.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-")
}

func tableCells(l string) []string {
	l = strings.TrimSpace(l)
	l = strings.TrimPrefix(l, "|")
	if strings.HasSuffix(l, "|") && !strings.HasSuffix(l, `\|`) {
		l = l[:len(l)-1]
	}
	var cells []string
	cur := strings.Builder{}
	inCode := false
	for i := 0; i < len(l); i++ {
		switch {
		case l[i] == '\\' && i+1 < len(l) && l[i+1] == '|':
			cur.WriteByte('|')
			i++
		case l[i] == '`':
			inCode = !inCode
			cur.WriteByte('`')
		case l[i] == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(convertHTML(cur.String())))
			cur.Reset()
		default:
			cur.WriteByte(l[i])
		}
	}
	return append(cells, strings.TrimSpace(convertHTML(cur.String())))
}

// startsBlock reports whether line i begins something other than a
// paragraph line (so a paragraph ends before it).
func startsBlock(lines []string, i int, t string) bool {
	switch {
	case t == "":
		return true
	case strings.HasPrefix(t, "#") && reHeading.MatchString(t):
		return true
	case fenceOf(t) != "", strings.HasPrefix(t, ":::"), strings.HasPrefix(t, ">"):
		return true
	case reHR.MatchString(t), reItem.MatchString(lines[i]):
		return true
	case strings.HasPrefix(strings.ToLower(t), "<table"):
		return true
	case isTableStart(lines, i):
		return true
	}
	return false
}

// joinText adds a source line to paragraph text: a line ending in two
// spaces or a backslash breaks the line there.
func joinText(text, line string) string {
	brk := strings.HasSuffix(line, "  ")
	line = strings.TrimSpace(line)
	if strings.HasSuffix(line, `\`) && !strings.HasSuffix(line, `\\`) {
		brk = true
		line = line[:len(line)-1]
	}
	if brk && line != "" {
		line += hardBreak
	}
	switch {
	case text == "":
		return line
	case line == "":
		return text
	case strings.HasSuffix(text, hardBreak):
		return text + line
	}
	return text + " " + line
}

// endText drops a line break left at the end of a paragraph.
func endText(text string) string {
	for strings.HasSuffix(text, hardBreak) {
		text = strings.TrimSuffix(text, hardBreak)
	}
	return strings.TrimSpace(text)
}

func indentOf(l string) int {
	n := 0
	for _, c := range l {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// parseBlocks parses prepared lines (raw: HTML is converted here).
func parseBlocks(raw []string) []Block {
	var out []Block
	var indents []int // open list levels, by indent
	listLevel := func(n int) int {
		for len(indents) > 0 && indents[len(indents)-1] > n {
			indents = indents[:len(indents)-1]
		}
		if len(indents) == 0 || indents[len(indents)-1] < n {
			indents = append(indents, n)
		}
		return len(indents) - 1
	}
	// the level a line indented by n continues, or -1
	contLevel := func(n int) int {
		for k := len(indents) - 1; k >= 0; k-- {
			if n > indents[k] {
				return k
			}
		}
		return -1
	}
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = convertHTML(l)
	}
	inList := false // the last block was a list item (no blank line since)
	for i := 0; i < len(lines); {
		rawT := strings.TrimSpace(raw[i])
		l := lines[i]
		t := strings.TrimSpace(l)
		if t == "" && !strings.HasPrefix(strings.ToLower(rawT), "<table") && !reCards.MatchString(rawT) {
			inList = false
			i++
			continue
		}
		level := -1 // list level a block nested under an item gets
		if len(indents) > 0 && indentOf(l) >= 2 && !reItem.MatchString(l) {
			level = contLevel(indentOf(l))
		}
		if level < 0 && !reItem.MatchString(l) {
			indents = indents[:0]
		}
		switch {
		case fenceOf(rawT) != "":
			fence := fenceOf(rawT)
			ind := indentOf(raw[i])
			var code []string
			i++
			for ; i < len(raw); i++ {
				tt := strings.TrimSpace(raw[i])
				if strings.HasPrefix(tt, fence) && strings.Trim(tt, fence[:1]) == "" {
					i++
					break
				}
				c := raw[i]
				if indentOf(c) >= ind { // drop the fence's own indent
					c = trimIndent(c, ind)
				}
				code = append(code, strings.ReplaceAll(c, "\t", "    "))
			}
			for len(code) > 0 && strings.TrimSpace(code[len(code)-1]) == "" {
				code = code[:len(code)-1]
			}
			out = append(out, Block{Kind: Code, Lines: code, Level: level})
			inList = false
		case strings.HasPrefix(strings.ToLower(rawT), "<table"):
			var b strings.Builder
			j := i
			for ; j < len(raw); j++ {
				b.WriteString(raw[j])
				b.WriteByte('\n')
				if strings.Contains(strings.ToLower(raw[j]), "</table>") {
					j++
					break
				}
			}
			html := b.String()
			low := strings.ToLower(html)
			if !strings.Contains(low, "<tr") || strings.Contains(html, "```") || strings.Contains(html, "~~~") {
				// a table used only for layout (around a code block): skip
				// its tags and read what is inside
				i++
				break
			}
			if rows, head := htmlTable(html); len(rows) > 0 {
				out = append(out, Block{Kind: Table, Rows: rows, Head: head})
			}
			i = j
			inList = false
		case reCards.MatchString(rawT):
			out = append(out, Block{Kind: Cards})
			i++
			inList = false
		case strings.HasPrefix(rawT, ":::") && reAdmon.MatchString(rawT):
			m := reAdmon.FindStringSubmatch(rawT)
			title := m[2]
			if title == "" {
				title = m[3]
			}
			depth := 1
			var inner []string
			i++
			for ; i < len(raw); i++ {
				tt := strings.TrimSpace(raw[i])
				if strings.HasPrefix(tt, ":::") {
					if reAdmon.MatchString(tt) {
						depth++
					} else if strings.Trim(tt, ":") == "" {
						depth--
						if depth == 0 {
							i++
							break
						}
					}
				}
				inner = append(inner, raw[i])
			}
			out = append(out, Block{Kind: Callout, Note: strings.ToLower(m[1]), Text: strings.TrimSpace(convertHTML(title)), Kids: parseBlocks(dedent(inner)), Level: level})
			inList = false
		case rawT == ":::":
			i++ // a stray closing fence
		case strings.HasPrefix(rawT, ">"):
			// Keyed on the raw line, as the loop below is: a line that
			// starts with ">" only once its tags are gone is a paragraph.
			var inner []string
			for ; i < len(lines); i++ {
				tt := strings.TrimSpace(raw[i])
				if !strings.HasPrefix(tt, ">") {
					if tt == "" || startsBlock(lines, i, strings.TrimSpace(lines[i])) {
						break
					}
					inner = append(inner, tt) // lazy continuation
					continue
				}
				tt = strings.TrimPrefix(tt, ">")
				tt = strings.TrimPrefix(tt, " ")
				inner = append(inner, tt)
			}
			out = append(out, Block{Kind: Quote, Kids: parseBlocks(inner), Level: level})
			inList = false
		case reHeading.MatchString(t):
			m := reHeading.FindStringSubmatch(t)
			txt, id := headingID(m[2])
			if plain(txt) != "" {
				out = append(out, Block{Kind: Heading, Level: len(m[1]), Text: txt, Anchor: anchorOr(id, txt)})
			}
			i++
			inList = false
		case reHR.MatchString(t) && !reItem.MatchString(l):
			out = append(out, Block{Kind: Rule})
			i++
			inList = false
		case reItem.MatchString(l):
			m := reItem.FindStringSubmatch(l)
			lv := listLevel(indentOf(m[1]))
			marker := "•"
			if c := m[2][0]; c >= '0' && c <= '9' {
				marker = m[2][:len(m[2])-1] + "."
			}
			text := joinText("", m[3])
			i++
			for i < len(lines) {
				tt := strings.TrimSpace(lines[i])
				if tt == "" || startsBlock(lines, i, tt) {
					break
				}
				text = joinText(text, lines[i])
				i++
			}
			out = append(out, Block{Kind: Item, Level: lv, Marker: marker, Text: endText(text)})
			inList = true
		case reEmptyLi.MatchString(l) && !reHR.MatchString(t):
			i++
		case isTableStart(lines, i):
			rows := [][]string{tableCells(lines[i])}
			head := []bool{true}
			i += 2
			for ; i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != ""; i++ {
				rows = append(rows, tableCells(lines[i]))
				head = append(head, false)
			}
			out = append(out, Block{Kind: Table, Rows: rows, Head: head})
			inList = false
		default:
			text := ""
			for i < len(lines) {
				tt := strings.TrimSpace(lines[i])
				if text != "" && startsBlock(lines, i, tt) {
					break
				}
				if tt == "" {
					break
				}
				text = joinText(text, lines[i])
				i++
			}
			text = endText(text)
			if plain(text) == "" {
				break // only pictures without a description, or stray markup
			}
			if inList && len(out) > 0 && out[len(out)-1].Kind == Item {
				// a lazy continuation of the item before
				out[len(out)-1].Text = joinText(out[len(out)-1].Text, text)
				break
			}
			if level >= 0 {
				out = append(out, Block{Kind: Item, Level: level, Text: text})
			} else {
				out = append(out, Block{Kind: Para, Text: text})
			}
		}
	}
	return out
}

func trimIndent(l string, n int) string {
	for n > 0 && len(l) > 0 && (l[0] == ' ' || l[0] == '\t') {
		l = l[1:]
		n--
	}
	return l
}

// dedent removes the indent common to all non-blank lines (a callout
// inside a list item is indented with it).
func dedent(lines []string) []string {
	n := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if k := indentOf(l); n < 0 || k < n {
			n = k
		}
	}
	if n <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = trimIndent(l, n)
	}
	return out
}

// anchorOr is the heading's id: the explicit {#id}, or Docusaurus's slug of
// its text.
func anchorOr(id, text string) string {
	if id != "" {
		return id
	}
	return Slugify(plain(text))
}

// Slugify makes a heading id the way Docusaurus (github-slugger) does:
// lower case, punctuation dropped, spaces to dashes.
func Slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r > 127 && isLetter(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isLetter(r rune) bool {
	return strings.ContainsRune("àáâäãåçèéêëìíîïñòóôöõùúûüýÿæœß", r) || (r >= 0x0400 && r <= 0x04ff) || (r >= 0x3040 && r <= 0x9fff)
}

// ---- inline ----

// Style is how a Span is drawn.
type Style uint8

const (
	Bold Style = 1 << iota
	Mono
	Faded // picture placeholders
)

// Span is a piece of text with one style; Link is set inside a link.
type Span struct {
	Text  string
	Style Style
	Link  string
}

// Inline parses inline Markdown: `code`, **bold**, *emphasis* (shown
// plain), [links](url), ![pictures](url) (as "[image: alt]"), <autolinks>
// and \escapes.
func Inline(s string) []Span {
	var out []Span
	inline(s, 0, "", &out)
	// merge neighbours with the same style
	m := out[:0]
	for _, sp := range out {
		if sp.Text == "" {
			continue
		}
		if n := len(m); n > 0 && m[n-1].Style == sp.Style && m[n-1].Link == sp.Link {
			m[n-1].Text += sp.Text
			continue
		}
		m = append(m, sp)
	}
	return m
}

// plain is the text of inline Markdown without its markup.
func plain(s string) string {
	var b strings.Builder
	for _, sp := range Inline(s) {
		b.WriteString(sp.Text)
	}
	return strings.TrimSpace(strings.ReplaceAll(b.String(), hardBreak, " "))
}

var reAutolink = regexp.MustCompile(`^<(https?://[^>\s]+)>`)

func isPunct(c byte) bool { return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0 }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

func isWordChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

func inline(s string, st Style, link string, out *[]Span) {
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			*out = append(*out, Span{Text: html.UnescapeString(buf.String()), Style: st, Link: link})
			buf.Reset()
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isPunct(s[i+1]):
			buf.WriteByte(s[i+1])
			i += 2
			continue
		case c == '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			if j := closingTicks(s, i+n, n); j >= 0 {
				flush()
				code := s[i+n : j]
				if len(code) > 2 && code[0] == ' ' && code[len(code)-1] == ' ' {
					code = code[1 : len(code)-1]
				}
				*out = append(*out, Span{Text: code, Style: st | Mono, Link: link})
				i = j + n
				continue
			}
			buf.WriteString(s[i : i+n])
			i += n
			continue
		case c == '!' && i+1 < len(s) && s[i+1] == '[':
			if alt, _, end, ok := linkAt(s, i+1); ok {
				flush()
				if a := plain(alt); a != "" {
					*out = append(*out, Span{Text: "[image: " + a + "]", Style: st | Faded, Link: link})
				}
				i = end
				continue
			}
		case c == '[':
			if text, url, end, ok := linkAt(s, i); ok {
				flush()
				if url == "" || link != "" {
					inline(text, st, link, out)
				} else {
					inline(text, st, url, out)
				}
				i = end
				continue
			}
		case c == '<':
			if m := reAutolink.FindStringSubmatch(s[i:]); m != nil {
				flush()
				*out = append(*out, Span{Text: m[1], Style: st, Link: m[1]})
				i += len(m[0])
				continue
			}
		case c == '*' || c == '_' || c == '~':
			n := 1
			for i+n < len(s) && s[i+n] == c {
				n++
			}
			if j, k := closingDelim(s, i, n); j >= 0 {
				flush()
				ns := st
				if k >= 2 && c != '~' {
					ns |= Bold
				}
				inline(s[i+k:j], ns, link, out)
				i = j + k
				continue
			}
			buf.WriteString(s[i : i+n])
			i += n
			continue
		}
		buf.WriteByte(c)
		i++
	}
	flush()
}

func closingTicks(s string, from, n int) int {
	for j := from; j < len(s); {
		if s[j] != '`' {
			j++
			continue
		}
		m := 0
		for j+m < len(s) && s[j+m] == '`' {
			m++
		}
		if m == n {
			return j
		}
		j += m
	}
	return -1
}

// closingDelim finds the end of an emphasis opened at i by a run of n
// marker characters. It returns where the closing run starts and the
// marker length used, or -1.
func closingDelim(s string, i, n int) (int, int) {
	c := s[i]
	k := n
	if k > 3 {
		return -1, 0
	}
	if c == '~' && k != 2 {
		return -1, 0
	}
	open := i + k
	if open >= len(s) || isSpace(s[open]) {
		return -1, 0
	}
	if c == '_' && i > 0 && isWordChar(s[i-1]) {
		return -1, 0 // snake_case
	}
	marker := s[i:open]
	for j := open + 1; j <= len(s)-k; j++ {
		if s[j] == '`' { // don't look inside code
			if e := closingTicks(s, j+1, 1); e >= 0 {
				j = e
				continue
			}
		}
		if s[j:j+k] != marker || isSpace(s[j-1]) {
			continue
		}
		if j+k < len(s) && s[j+k] == c {
			continue // part of a longer run
		}
		if c == '_' && j+k < len(s) && isWordChar(s[j+k]) {
			continue
		}
		return j, k
	}
	return -1, 0
}

// linkAt reads [text](url "title") at s[i] == '['.
func linkAt(s string, i int) (text, url string, end int, ok bool) {
	depth := 0
	j := i
	for ; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '[':
			depth++
		case ']':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	if j >= len(s) || j+1 >= len(s) || s[j+1] != '(' {
		return
	}
	text = s[i+1 : j]
	depth = 0
	k := j + 1
	for ; k < len(s); k++ {
		switch s[k] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	if k >= len(s) {
		return
	}
	url = strings.TrimSpace(s[j+2 : k])
	if sp := strings.IndexAny(url, " \t"); sp >= 0 { // drop a "title"
		url = url[:sp]
	}
	url = strings.TrimSuffix(strings.TrimPrefix(url, "<"), ">")
	return text, url, k + 1, true
}

// PlainText is a page's text without markup, for search.
func PlainText(blocks []Block) string {
	var b strings.Builder
	var walk func([]Block)
	walk = func(bs []Block) {
		for _, bl := range bs {
			switch bl.Kind {
			case Code:
				b.WriteString(strings.Join(bl.Lines, " "))
			case Table:
				for _, r := range bl.Rows {
					for _, c := range r {
						b.WriteString(plain(c))
						b.WriteByte(' ')
					}
				}
			case Callout, Quote:
				if bl.Text != "" {
					b.WriteString(plain(bl.Text))
					b.WriteByte(' ')
				}
				walk(bl.Kids)
			default:
				b.WriteString(plain(bl.Text))
			}
			b.WriteByte('\n')
		}
	}
	walk(blocks)
	return b.String()
}
