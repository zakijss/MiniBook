// MiniBook reads the Onion OS documentation (the website/docs folder of
// OnionUI/Onion) on the Miyoo. Every page it opens is kept on the card, and
// "Download all for offline" saves the rest, so it works without Wi-Fi.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zakijss/minideck/appdir"
	"github.com/zakijss/minideck/tnet"
	"github.com/zakijss/minideck/ui"
	"github.com/zakijss/minideck/version"
)

// State is what MiniBook remembers between runs: the page last read and where
// each page was scrolled to.
type State struct {
	Last string         `json:"last"`
	Tops map[string]int `json:"tops"`
}

type app struct {
	scr        *ui.Screen
	input      <-chan ui.Event
	d          *Docs
	poke       chan struct{}
	dir        string
	cfg        Settings
	st         State
	pages      []*Page // laid out recently, newest last
	viewH      int     // the reader's text height, from the last frame
	prefetched map[string]bool
	query      string
	savedAt    time.Time
}

func main() {
	version.Flag("minibook")
	version.Note("minibook")
	scr, err := ui.Open()
	if err != nil {
		log.Fatal(err)
	}
	defer scr.Close()
	dir := appdir.Dir()
	a := &app{scr: scr, input: ui.Input(), poke: make(chan struct{}, 1), dir: dir, viewH: 320, prefetched: map[string]bool{}}
	a.d = NewDocs(dir, tnet.Client(20*time.Second))
	a.d.Changed = a.redraw
	a.loadState()
	a.cfg = loadSettings(dir)
	go a.d.cache.Prune(32 << 20)
	if !a.d.HasTree() {
		err := a.wait("MiniBook", "Getting the list of pages", func(ctx context.Context) error { return a.d.RefreshTree(ctx) })
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				scr.Notice(a.input, "Can't reach GitHub", "MiniBook downloads the Onion docs the first time it opens. Connect to Wi-Fi and open it again. ("+tnet.Explain(err).Error()+")")
			}
			return
		}
	} else {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			a.d.RefreshTree(ctx)
			cancel()
		}()
	}
	if p := a.startPage(); p != "" {
		a.read(p, "")
	}
	a.home()
	a.saveState()
	a.d.SaveMeta()
}

// redraw asks the screen loop for a new frame; it never blocks.
func (a *app) redraw() {
	select {
	case a.poke <- struct{}{}:
	default:
	}
}

// next waits for a button, a change in the background, or a few seconds
// (for the clock). A zero Event means "just redraw".
func (a *app) next() (ui.Event, bool) {
	select {
	case ev, ok := <-a.input:
		return ev, ok
	case <-a.poke:
	case <-time.After(5 * time.Second):
	}
	return ui.Event{}, true
}

// sub adds "offline" to a subtitle while GitHub can't be reached.
func (a *app) sub(s string) string {
	if a.d.Offline() {
		if s == "" {
			return "Offline"
		}
		return s + " · offline"
	}
	return s
}

func (a *app) loadState() {
	if b, err := os.ReadFile(filepath.Join(a.dir, "state.json")); err == nil {
		json.Unmarshal(b, &a.st)
	}
	if a.st.Tops == nil {
		a.st.Tops = map[string]int{}
	}
}

func (a *app) saveState() {
	for k := range a.st.Tops { // keep it small
		if len(a.st.Tops) <= 64 {
			break
		}
		if k != a.st.Last {
			delete(a.st.Tops, k)
		}
	}
	if err := writeJSON(filepath.Join(a.dir, "state.json"), a.st); err != nil {
		log.Printf("save state: %v", err)
	}
	a.savedAt = time.Now()
}

// wait shows a busy screen while fn runs; B cancels it.
func (a *app) wait(title, msg string, fn func(context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- fn(ctx) }()
	for {
		s := a.scr
		top := s.StaticFrame(title, "", ui.Hint{Button: "B", Label: "Cancel"})
		s.Loading(top+130, msg)
		s.Present()
		select {
		case err := <-errc:
			return err
		case ev, ok := <-a.input:
			if !ok || ev.Pressed && (ev.Button == ui.B || ev.Button == ui.Menu) {
				cancel()
				<-errc
				return context.Canceled
			}
		case <-time.After(time.Second):
		}
	}
}

// ---- lists ----

func (a *app) item(n *Node) ui.Item {
	if n.IsList() {
		sub := n.Desc
		if sub == "" {
			sub = fmt.Sprintf("%d pages", n.Count())
		}
		return ui.Item{Label: sanitize(ui.Heading, n.Label), Sub: sanitize(ui.Small, sub), Chevron: true}
	}
	it := ui.Item{Label: sanitize(ui.Heading, n.Label), Sub: sanitize(ui.Small, n.Desc)}
	if a.d.Offline() && !a.d.IsSaved(n.Page) {
		it.Detail = "Not saved"
	}
	return it
}

func (a *app) open(n *Node) {
	if n.IsList() {
		a.list(n.Dir)
	} else {
		a.read(n.Page, "")
	}
}

func (a *app) home() {
	l := ui.List{}
	// the sections' own pages give them their titles
	var firsts []string
	for _, n := range a.d.Nav().Kids {
		if n.Page != "" {
			firsts = append(firsts, n.Page)
		}
	}
	a.d.Prefetch(firsts)
	for {
		nav := a.d.Nav()
		var items []ui.Item
		var nodes []*Node // nil for the extra rows
		cont := a.st.Last != "" && a.d.sha(a.st.Last) != ""
		if cont {
			items = append(items, ui.Item{Label: "Continue reading", Sub: sanitize(ui.Small, a.d.Title(a.st.Last)), Chevron: true})
			nodes = append(nodes, nil)
		}
		for _, n := range nav.Kids {
			items = append(items, a.item(n))
			nodes = append(nodes, n)
		}
		saved, total := a.d.Saved()
		dl := fmt.Sprintf("%d of %d pages saved", saved, total)
		if saved == total {
			dl = fmt.Sprintf("All %d pages saved", total)
		}
		items = append(items, ui.Item{Label: "Download all for offline", Sub: dl})
		nodes = append(nodes, nil)
		l.Items = items
		l.Sel = min(l.Sel, len(items)-1)
		s := a.scr
		top := s.StaticFrame("MiniBook", a.sub("Onion OS documentation"),
			ui.Hint{Button: "A", Label: "Open"}, ui.Hint{Button: "Y", Label: "Search"}, ui.Hint{Button: "START", Label: "Settings"}, ui.Hint{Button: "B", Label: "Exit"})
		l.Draw(s, image.Rect(20, top, ui.W-26, ui.H-58))
		s.Present()
		ev, ok := a.next()
		if !ok {
			return
		}
		if !ev.Pressed { // a redraw, or a release (scripted input)
			continue
		}
		switch ev.Button {
		case ui.B, ui.Menu:
			return
		case ui.A:
			switch {
			case cont && l.Sel == 0:
				a.read(a.st.Last, "")
			case l.Sel == len(items)-1:
				a.downloadAll()
			default:
				a.open(nodes[l.Sel])
			}
		case ui.Y:
			if p, q, ok := a.search(); ok {
				a.read(p, q)
			}
		case ui.Select: // also in Settings, which the footer shows instead
			a.look()
		case ui.Start:
			a.settings()
		default:
			l.Handle(ev)
		}
	}
}

// look opens Appearance. Laid-out pages keep the colours of the theme they
// were laid out in, so after a change they are laid out again when next
// opened.
func (a *app) look() {
	before := ui.CurrentAppearance()
	a.scr.Appearance(a.input)
	if ui.CurrentAppearance() != before {
		a.pages = nil
	}
}

// list shows a category's pages (and its sub-categories).
func (a *app) list(dir string) {
	l := ui.List{}
	for {
		n := a.d.Find(dir)
		if n == nil {
			return
		}
		var items []ui.Item
		var nodes []*Node
		if n.Page != "" {
			// the category's own page comes first
			self := &Node{Page: n.Page, Label: a.d.Title(n.Page), Desc: n.Desc}
			items = append(items, a.item(self))
			nodes = append(nodes, self)
		}
		for _, k := range n.Kids {
			items = append(items, a.item(k))
			nodes = append(nodes, k)
		}
		if !a.prefetched[dir] {
			// save the pages listed here in the background: their real
			// titles appear, and they can be read offline later
			a.prefetched[dir] = true
			var ps []string
			for _, k := range nodes {
				if k.Page != "" {
					ps = append(ps, k.Page)
				}
			}
			a.d.Prefetch(ps)
		}
		l.Items = items
		l.Sel = min(l.Sel, len(items)-1)
		s := a.scr
		top := s.StaticFrame(sanitize(ui.Title, n.Label), a.sub(fmt.Sprintf("%d pages", n.Count())),
			ui.Hint{Button: "A", Label: "Open"}, ui.Hint{Button: "Y", Label: "Search"}, ui.Hint{Button: "B", Label: "Back"})
		l.Draw(s, image.Rect(20, top, ui.W-26, ui.H-58))
		s.Present()
		ev, ok := a.next()
		if !ok {
			return
		}
		if !ev.Pressed { // a redraw, or a release (scripted input)
			continue
		}
		switch ev.Button {
		case ui.B, ui.Menu:
			return
		case ui.A:
			if l.Sel < len(nodes) {
				a.open(nodes[l.Sel])
			}
		case ui.Y:
			if p, q, ok := a.search(); ok {
				a.read(p, q)
			}
		default:
			l.Handle(ev)
		}
	}
}

// ---- reader ----

// Text column: inside the card from x 20 to W-26, with 18 px margins.
const (
	cardX0 = 20
	cardX1 = ui.W - 26
	textX  = cardX0 + 18
	textW  = cardX1 - 18 - textX
)

// load returns a page laid out, downloading it first if it isn't saved.
func (a *app) load(p string) *Page {
	for i, pg := range a.pages {
		if pg.Path == p {
			a.pages = append(append(a.pages[:i:i], a.pages[i+1:]...), pg)
			return pg
		}
	}
	body, fresh, ok := a.d.Cached(p)
	if !ok {
		err := a.wait(sanitize(ui.Title, a.d.Title(p)), "Loading", func(ctx context.Context) error {
			b, err := a.d.Get(ctx, p)
			body = b
			return err
		})
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				msg := tnet.Explain(err).Error()
				if a.d.Offline() {
					msg = "This page isn't saved on the card, and GitHub can't be reached. Next time you have Wi-Fi, use Download all for offline."
				}
				a.scr.Notice(a.input, "Can't open this page", msg)
			}
			return nil
		}
	} else if !fresh && !a.d.Offline() {
		// a newer version exists: fetch it for next time
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			a.d.Fetch(ctx, p)
			cancel()
			a.d.SaveMeta()
		}()
	}
	doc := Parse(body)
	doc.Blocks = a.cards(p, doc.Blocks)
	pg := Layout(doc, textW)
	pg.Path = p
	pg.Title = firstOf(doc.Title, a.d.Title(p))
	a.pages = append(a.pages, pg)
	if len(a.pages) > 4 {
		a.pages = a.pages[1:]
	}
	return pg
}

// startTop picks the first line to show: a heading, a search hit, or where
// the page was left.
func (a *app) startTop(pg *Page, find, anchor string) int {
	top := a.st.Tops[pg.Path]
	if find != "" || anchor != "" {
		top = 0
	}
	if anchor != "" {
		if l, ok := pg.Anchors[anchor]; ok {
			top = l
		}
	}
	if find != "" {
		if l := pg.find(find); l >= 0 {
			top = max(0, l-1)
		}
	}
	return min(max(0, top), pg.maxTop(a.viewH))
}

type loc struct {
	path string
	top  int
}

func (a *app) read(p, find string) {
	pg := a.load(p)
	if pg == nil {
		return
	}
	top := a.startTop(pg, find, "")
	var back []loc // pages left by following a link
	step := 3 * lineH(ui.Body)
	goTo := func(target, q, anchor string) {
		npg := pg
		if target != pg.Path {
			npg = a.load(target)
			if npg == nil {
				return
			}
			back = append(back, loc{pg.Path, top})
		}
		pg = npg
		top = a.startTop(pg, q, anchor)
	}
	for {
		if a.st.Last != pg.Path || a.st.Tops[pg.Path] != top {
			a.st.Last = pg.Path
			a.st.Tops[pg.Path] = top
			// keep the place even if the Miyoo is switched off mid-page,
			// without writing to the card on every press
			if time.Since(a.savedAt) > 20*time.Second {
				a.saveState()
			}
		}
		a.drawPage(pg, top)
		ev, ok := a.next()
		if !ok {
			return
		}
		if !ev.Pressed { // a redraw, or a release (scripted input)
			continue
		}
		h := a.viewH
		switch ev.Button {
		case ui.Down:
			top = pg.scroll(top, step, h)
		case ui.Up:
			top = pg.scroll(top, -step, h)
		case ui.R1, ui.Right:
			top = pg.pageDown(top, h)
		case ui.L1, ui.Left:
			top = pg.pageUp(top, h)
		case ui.R2:
			top = pg.maxTop(h)
		case ui.L2:
			top = 0
		case ui.A:
			if target, anchor, ok := a.pickLink(pg, top); ok {
				goTo(target, "", anchor)
			}
		case ui.Y:
			if target, q, ok := a.search(); ok {
				goTo(target, q, "")
			}
		case ui.B:
			a.saveState()
			if len(back) == 0 {
				return
			}
			l := back[len(back)-1]
			back = back[:len(back)-1]
			if npg := a.load(l.path); npg != nil {
				pg, top = npg, min(l.top, npg.maxTop(h))
			}
		case ui.Menu:
			a.saveState()
			return
		}
	}
}

func (a *app) drawPage(pg *Page, top int) {
	s := a.scr
	hints := []ui.Hint{{Button: "L1/R1", Label: "Page"}}
	if len(pg.Links) > 0 {
		hints = append(hints, ui.Hint{Button: "A", Label: "Links"})
	}
	hints = append(hints, ui.Hint{Button: "Y", Label: "Search"}, ui.Hint{Button: "B", Label: "Back"})
	sub := a.d.Breadcrumb(pg.Path)
	if sub == "" {
		sub = "Onion OS documentation"
	}
	y0 := s.StaticFrame(sanitize(ui.Title, pg.Title), sanitize(ui.Small, a.sub(sub)), hints...)
	card := image.Rect(cardX0, y0, cardX1, ui.H-58)
	s.Panel(card, 18)
	ty := card.Min.Y + 12
	h := card.Max.Y - 12 - ty
	a.viewH = h
	if len(pg.Lines) == 0 {
		s.Present()
		return
	}
	top = min(max(top, 0), len(pg.Lines)-1)
	last := top + pg.fits(top, h) - 1
	baseY := pg.Lines[top].Y
	yEnd := ty + pg.Lines[last].Y + pg.Lines[last].H - baseY
	clip := image.Rect(card.Min.X, ty, card.Max.X, yEnd)
	for _, b := range pg.Boxes {
		if b.To < top || b.From > last {
			continue
		}
		r := image.Rect(textX+b.X0, ty+pg.Lines[b.From].Y-baseY, textX+b.X1, ty+pg.Lines[b.To].Y+pg.Lines[b.To].H-baseY)
		drawBox(s, clip, r, b.Kind)
	}
	for i := top; i <= last; i++ {
		l := pg.Lines[i]
		y := ty + l.Y - baseY
		if l.Rule[1] > l.Rule[0] {
			tint(s.RGBA, image.Rect(textX+l.Rule[0], y+l.H/2, textX+l.Rule[1], y+l.H/2+1), ui.Text, 0.16)
		}
		for _, r := range l.Runs {
			drawRun(s, y, l.H, r)
		}
	}
	if pg.Height > h { // scroll bar
		th := max(24, card.Dy()*h/pg.Height)
		end := max(1, pg.Lines[pg.maxTop(h)].Y)
		ty := card.Min.Y + (card.Dy()-th)*min(baseY, end)/end
		s.RoundRectAlpha(image.Rect(card.Max.X+4, ty, card.Max.X+7, ty+th), 2, ui.Text, 0.4)
	}
	s.Present()
}

func drawRun(s *ui.Screen, y, lh int, r Run) {
	fh := ui.LineHeight(r.Face)
	fy := y + (lh-fh)/2
	x := textX + r.X
	if r.Deco&decoCode != 0 {
		s.RoundRectAlpha(image.Rect(x-4, fy+1, x+r.W+4, fy+fh), 5, ui.Text, 0.14)
	}
	if r.Deco&decoMono != 0 {
		for i, c := range []rune(r.Text) {
			if c != ' ' {
				cx := x + i*monoCell + (monoCell-measure(r.Face, string(c)))/2
				s.Text(cx, fy, string(c), r.Face, r.Color)
			}
		}
		return
	}
	s.Text(x, fy, r.Text, r.Face, r.Color)
	if r.Deco&decoLink != 0 {
		u := fy + r.Face.Metrics().Ascent.Ceil() + 3
		tint(s.RGBA, image.Rect(x, u, x+r.W, u+1), linkColor, 0.6)
	}
}

func drawBox(s *ui.Screen, clip, r image.Rectangle, kind string) {
	switch kind {
	case "code":
		roundBox(s, clip, r, 10, ui.Text, 0.08)
	case "quote":
		roundBox(s, clip, image.Rect(r.Min.X, r.Min.Y+2, r.Min.X+4, r.Max.Y-2), 2, ui.Muted, 1)
	default:
		col := noteColor(kind)
		roundBox(s, clip, r, 12, col, 0.13)
		roundBox(s, clip, image.Rect(r.Min.X+10, r.Min.Y+12, r.Min.X+14, r.Max.Y-12), 2, col, 1)
	}
}

// roundBox fills r (cut to clip) with c at opacity alpha, rounding only the
// corners that show. The flat middle is blended row by row, which is far
// cheaper than the per-pixel rounded fill.
func roundBox(s *ui.Screen, clip, r image.Rectangle, rad int, c color.RGBA, alpha float64) {
	v := r.Intersect(clip)
	if v.Empty() {
		return
	}
	rad = min(rad, r.Dy()/2, r.Dx()/2)
	topShows := r.Min.Y >= clip.Min.Y
	botShows := r.Max.Y <= clip.Max.Y
	y0, y1 := v.Min.Y, v.Max.Y
	if topShows {
		y0 = r.Min.Y + rad
	}
	if botShows {
		y1 = r.Max.Y - rad
	}
	if y1 > y0 {
		tint(s.RGBA, image.Rect(r.Min.X, y0, r.Max.X, y1).Intersect(clip), c, alpha)
	}
	if rad <= 0 {
		return
	}
	if topShows {
		sub := clipped(s, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+rad).Intersect(clip))
		sub.RoundRectAlpha(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+2*rad), rad, c, alpha)
	}
	if botShows {
		sub := clipped(s, image.Rect(r.Min.X, r.Max.Y-rad, r.Max.X, r.Max.Y).Intersect(clip))
		sub.RoundRectAlpha(image.Rect(r.Min.X, r.Max.Y-2*rad, r.Max.X, r.Max.Y), rad, c, alpha)
	}
}

// clipped is a Screen that draws only inside r.
func clipped(s *ui.Screen, r image.Rectangle) *ui.Screen {
	return &ui.Screen{RGBA: s.RGBA.SubImage(r).(*image.RGBA)}
}

// tint blends c over r at opacity a, in integer maths.
func tint(img *image.RGBA, r image.Rectangle, c color.RGBA, a float64) {
	r = r.Intersect(img.Rect)
	if r.Empty() {
		return
	}
	ia := uint32(a*256 + 0.5)
	if ia >= 256 {
		ia = 256
	}
	na := 256 - ia
	cr, cg, cb := uint32(c.R)*ia, uint32(c.G)*ia, uint32(c.B)*ia
	for y := r.Min.Y; y < r.Max.Y; y++ {
		p := img.Pix[img.PixOffset(r.Min.X, y):img.PixOffset(r.Max.X, y)]
		for i := 0; i+3 < len(p); i += 4 {
			p[i] = uint8((uint32(p[i])*na + cr) >> 8)
			p[i+1] = uint8((uint32(p[i+1])*na + cg) >> 8)
			p[i+2] = uint8((uint32(p[i+2])*na + cb) >> 8)
		}
	}
}

// pickLink offers the in-doc links on screen (or on the page, if none
// show) and returns where the chosen one goes.
func (a *app) pickLink(pg *Page, top int) (string, string, bool) {
	last := top + pg.fits(top, a.viewH) - 1
	collect := func(all bool) []PageLink {
		var out []PageLink
		seen := map[string]bool{}
		for _, l := range pg.Links {
			if (!all && (l.Line < top || l.Line > last)) || seen[l.URL] || l.Text == "" {
				continue
			}
			seen[l.URL] = true
			out = append(out, l)
		}
		return out
	}
	links := collect(false)
	if len(links) == 0 {
		links = collect(true)
	}
	if len(links) == 0 {
		return "", "", false
	}
	pick := 0
	if len(links) > 1 {
		items := make([]ui.Item, len(links))
		for i, l := range links {
			sub := "Not in the docs"
			if target, anchor, ok := a.d.Resolve(pg.Path, l.URL); ok {
				sub = a.d.Title(target)
				if target == pg.Path {
					sub = "On this page"
				}
				if anchor != "" {
					sub += " › " + strings.ReplaceAll(anchor, "-", " ")
				}
			}
			items[i] = ui.Item{Label: sanitize(ui.Heading, l.Text), Sub: sanitize(ui.Small, sub), Chevron: true}
		}
		i, ok := a.scr.Choose(a.input, "Links", items)
		if !ok {
			return "", "", false
		}
		pick = i
	}
	target, anchor, ok := a.d.Resolve(pg.Path, links[pick].URL)
	if !ok {
		a.scr.Notice(a.input, "Not in the docs", "MiniBook can't find the page this link points to ("+links[pick].URL+").")
		return "", "", false
	}
	return target, anchor, true
}

// ---- search ----

func (a *app) search() (string, string, bool) {
	q, ok := a.scr.Prompt(a.input, "Search", a.query, "Page titles, and the text of saved pages", false)
	q = strings.TrimSpace(q)
	if !ok || q == "" {
		return "", "", false
	}
	a.query = q
	var hits []Hit
	if err := a.wait("Search", "Searching", func(ctx context.Context) error {
		hits = a.d.Search(ctx, q)
		return nil
	}); err != nil {
		return "", "", false
	}
	if len(hits) == 0 {
		msg := "No page mentions “" + q + "”."
		if saved, total := a.d.Saved(); saved < total {
			msg += fmt.Sprintf(" Only saved pages (%d of %d) are searched inside: Download all for offline saves the rest.", saved, total)
		}
		a.scr.Notice(a.input, "No results", msg)
		return "", "", false
	}
	items := make([]ui.Item, len(hits))
	for i, h := range hits {
		items[i] = ui.Item{Label: sanitize(ui.Heading, h.Title), Sub: sanitize(ui.Small, h.Snippet)}
	}
	l := ui.List{Items: items}
	for {
		s := a.scr
		n := "1 result"
		if len(hits) != 1 {
			n = fmt.Sprintf("%d results", len(hits))
		}
		top := s.StaticFrame("Search", a.sub(n+" for “"+q+"”"), ui.Hint{Button: "A", Label: "Open"}, ui.Hint{Button: "B", Label: "Back"})
		l.Draw(s, image.Rect(20, top, ui.W-26, ui.H-58))
		s.Present()
		ev, ok := a.next()
		if !ok {
			return "", "", false
		}
		if !ev.Pressed {
			continue
		}
		switch ev.Button {
		case ui.A:
			return hits[l.Sel].Path, q, true
		case ui.B, ui.Menu:
			return "", "", false
		default:
			l.Handle(ev)
		}
	}
}

// ---- download all ----

func (a *app) downloadAll() {
	var mu sync.Mutex
	done, total := 0, -1
	var failed int
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		f, err := a.d.DownloadAll(ctx, func(d, t int) {
			mu.Lock()
			done, total = d, t
			mu.Unlock()
			a.redraw()
		})
		failed = f
		errc <- err
	}()
	for {
		mu.Lock()
		d, t := done, total
		mu.Unlock()
		s := a.scr
		top := s.StaticFrame("Download all", "Saving every page for reading offline", ui.Hint{Button: "B", Label: "Cancel"})
		bar := image.Rect(80, top+140, ui.W-80, top+150)
		frac := 0.0
		if t > 0 {
			frac = float64(d) / float64(t)
		}
		s.Bar(bar, frac, ui.Text)
		label := "Checking for changes…"
		if t >= 0 {
			label = fmt.Sprintf("%d of %d pages", d, t)
		}
		s.TextCenter(ui.W/2, top+166, label, ui.Small, ui.Muted)
		s.Present()
		select {
		case err := <-errc:
			saved, all := a.d.Saved()
			switch {
			case err != nil && failed == 0:
				a.scr.Notice(a.input, "Couldn't download", tnet.Explain(err).Error())
			case failed > 0:
				a.scr.Notice(a.input, "Some pages failed", fmt.Sprintf("%d couldn't be downloaded (%s). %d of %d pages are saved: try again later for the rest.", failed, tnet.Explain(err).Error(), saved, all))
			case t == 0:
				a.scr.Notice(a.input, "Up to date", fmt.Sprintf("All %d pages are already on the card.", all))
			default:
				a.scr.Notice(a.input, "Saved for offline", fmt.Sprintf("All %d pages are on the card. MiniBook now works without Wi-Fi.", all))
			}
			return
		case ev, ok := <-a.input:
			if !ok || ev.Pressed && (ev.Button == ui.B || ev.Button == ui.Menu) {
				cancel()
				<-errc
				return
			}
		case <-a.poke:
		case <-time.After(time.Second):
		}
	}
}

// cards fills in <DocCardList/>: a list of links to the pages of the
// category this page opens.
func (a *app) cards(p string, bs []Block) []Block {
	var out []Block
	for _, b := range bs {
		if b.Kind == Callout || b.Kind == Quote {
			b.Kids = a.cards(p, b.Kids)
		}
		if b.Kind != Cards {
			out = append(out, b)
			continue
		}
		var n *Node
		var find func(*Node)
		find = func(x *Node) {
			if x.cat && x.Page == p {
				n = x
			}
			for _, k := range x.Kids {
				if n == nil {
					find(k)
				}
			}
		}
		find(a.d.Nav())
		if n == nil {
			continue
		}
		for _, k := range n.Kids {
			label := strings.NewReplacer("[", "", "]", "").Replace(k.Label)
			text := label
			if k.Page != "" {
				text = "[" + label + "](/" + escapePath(k.Page) + ")"
			}
			if k.IsList() {
				text += fmt.Sprintf(" (%d pages)", k.Count())
			}
			out = append(out, Block{Kind: Item, Marker: "•", Text: text})
		}
	}
	return out
}
