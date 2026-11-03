package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zakijss/minideck/atomicfile"
	"github.com/zakijss/minideck/cache"
)

// Where the docs come from: the Onion repository's website/docs folder.
// The file list comes from GitHub's trees API (one request for the folder,
// about 80 KB), and each page from raw.githubusercontent.com.
var (
	treeURL = "https://api.github.com/repos/OnionUI/Onion/git/trees/main:website/docs?recursive=1"
	rawURL  = "https://raw.githubusercontent.com/OnionUI/Onion/main/website/docs/"
)

// Entry is a file in the docs folder; SHA is its git blob id, so a changed
// page is noticed without downloading it.
type Entry struct {
	Path string `json:"p"`
	SHA  string `json:"s"`
}

// Tree is the docs folder's file list as last fetched, with the ETag that
// lets the next fetch cost nothing when it hasn't changed.
type Tree struct {
	ETag    string    `json:"etag"`
	Fetched time.Time `json:"fetched"`
	Entries []Entry   `json:"entries"`
}

// Meta is what the navigation needs from a page (or a _category_.json),
// kept so lists show real titles without reading every page.
type Meta struct {
	Title  string  `json:"t,omitempty"`
	Label  string  `json:"l,omitempty"`
	Slug   string  `json:"s,omitempty"`
	Desc   string  `json:"d,omitempty"`
	Pos    float64 `json:"p,omitempty"`
	HasPos bool    `json:"h,omitempty"`
	Ver    string  `json:"v,omitempty"` // the blob saved on the card
}

// Node is an entry in the navigation: a category (Kids, and perhaps its
// own index Page) or a page.
type Node struct {
	Dir   string // the category's folder; "" for pages
	Page  string // the page to open: a page, or the category's index
	Label string
	Desc  string
	Kids  []*Node
	pos   float64
	name  string
	cat   bool
}

// Docs keeps the file list, the pages on the card and the navigation.
type Docs struct {
	dir   string
	cache *cache.Disk
	http  *http.Client
	// Changed is called (from any goroutine) when the navigation or the
	// online state changes.
	Changed func()

	mu     sync.Mutex
	tree   Tree
	meta   map[string]Meta
	nav    *Node
	slugs  map[string]string // slug -> page
	shas   map[string]string // page -> current blob
	status int               // 0 unknown, 1 online, 2 offline
	dirty  bool              // meta needs saving
	sem    chan struct{}
}

const (
	statusUnknown = iota
	statusOnline
	statusOffline
)

// NewDocs opens the docs saved in dir (the app's folder) and fetches with
// client.
func NewDocs(dir string, client *http.Client) *Docs {
	d := &Docs{dir: dir, cache: cache.New(filepath.Join(dir, "cache")), http: client, meta: map[string]Meta{}, sem: make(chan struct{}, 3)}
	if b, err := os.ReadFile(filepath.Join(dir, "tree.json")); err == nil {
		json.Unmarshal(b, &d.tree)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
		json.Unmarshal(b, &d.meta)
	}
	d.rebuild()
	return d
}

func (d *Docs) changed() {
	if d.Changed != nil {
		d.Changed()
	}
}

// writeJSON saves v to p so that a power cut leaves the old file or the
// new one.
func writeJSON(p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return atomicfile.Write(p, b, 0644)
}

// SaveMeta writes the page titles to the card, if they changed.
func (d *Docs) SaveMeta() {
	d.mu.Lock()
	if !d.dirty {
		d.mu.Unlock()
		return
	}
	d.dirty = false
	m := make(map[string]Meta, len(d.meta))
	for k, v := range d.meta {
		m[k] = v
	}
	d.mu.Unlock()
	if err := writeJSON(filepath.Join(d.dir, "meta.json"), m); err != nil {
		log.Printf("save page titles: %v", err)
	}
}

// rebuild remakes the navigation. Call with d.mu held, or before sharing d.
func (d *Docs) rebuild() {
	d.nav, d.slugs = buildNav(d.tree.Entries, d.meta)
	d.shas = make(map[string]string, len(d.tree.Entries))
	for _, e := range d.tree.Entries {
		d.shas[e.Path] = e.SHA
	}
}

// HasTree reports whether the file list has been fetched at least once.
func (d *Docs) HasTree() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.tree.Entries) > 0
}

// Nav returns the navigation's root.
func (d *Docs) Nav() *Node {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.nav
}

// Offline reports whether the last try to reach GitHub failed.
func (d *Docs) Offline() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status == statusOffline
}

func (d *Docs) setStatus(err error) {
	st := statusOnline
	var he httpError
	if err != nil && !errors.As(err, &he) && !errors.Is(err, context.Canceled) {
		st = statusOffline
	}
	d.mu.Lock()
	old := d.status
	d.status = st
	d.mu.Unlock()
	if old != st {
		d.changed()
	}
}

type httpError int

func (e httpError) Error() string {
	if e == 403 || e == 429 {
		return "GitHub is limiting requests: try again in an hour."
	}
	return fmt.Sprintf("GitHub answered HTTP %d.", int(e))
}

// maxReply caps one download: the file list or a page.
const maxReply = 4 << 20

func (d *Docs) get(ctx context.Context, u, etag string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, nil
	}
	if resp.StatusCode != 200 {
		return nil, "", httpError(resp.StatusCode)
	}
	// one byte over the cap tells a cut-off reply from one that fits, so a
	// truncated page is never saved as the current one
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err == nil && len(b) > maxReply {
		return nil, "", errors.New("GitHub's reply is too large")
	}
	return b, resp.Header.Get("ETag"), err
}

// isDoc reports whether a file is a page or a category of the docs (the
// folder also holds pictures and other assets).
func isDoc(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".mdx":
		return true
	}
	return path.Base(p) == "_category_.json"
}

// parseTree reads GitHub's trees reply, keeping only the docs.
func parseTree(b []byte) ([]Entry, error) {
	var t struct {
		Tree []struct {
			Path, Type, SHA string
		}
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range t.Tree {
		if e.Type == "blob" && isDoc(e.Path) {
			out = append(out, Entry{Path: e.Path, SHA: e.SHA})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no pages in the docs folder")
	}
	return out, nil
}

// RefreshTree gets the file list (a 304 when nothing changed costs nothing
// against GitHub's limit), then any _category_.json that changed.
func (d *Docs) RefreshTree(ctx context.Context) error {
	d.mu.Lock()
	etag := d.tree.ETag
	if len(d.tree.Entries) == 0 {
		etag = ""
	}
	d.mu.Unlock()
	b, newTag, err := d.get(ctx, treeURL, etag)
	d.setStatus(err)
	if err != nil {
		return err
	}
	d.mu.Lock()
	if b != nil {
		entries, err := parseTree(b)
		if err != nil {
			d.mu.Unlock()
			return err
		}
		d.tree.Entries = entries
		d.tree.ETag = newTag
	}
	d.tree.Fetched = time.Now()
	d.rebuild()
	t := d.tree
	d.mu.Unlock()
	if err := writeJSON(filepath.Join(d.dir, "tree.json"), t); err != nil {
		log.Printf("save file list: %v", err)
	}
	for _, p := range d.Missing() {
		if path.Base(p) == "_category_.json" {
			d.Fetch(ctx, p)
		}
	}
	d.SaveMeta()
	d.changed()
	return nil
}

func (d *Docs) sha(p string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.shas[p]
}

func cacheKey(p string) string { return "page:" + p }

// Cached returns the page saved on the card; fresh is false when the file
// list has a newer version.
func (d *Docs) Cached(p string) (body string, fresh, ok bool) {
	b, ok := d.cache.Get(cacheKey(p))
	if !ok {
		return "", false, false
	}
	sha, rest, _ := strings.Cut(string(b), "\n")
	return rest, sha == d.sha(p), true
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// Fetch downloads a page, saves it and notes its title.
func (d *Docs) Fetch(ctx context.Context, p string) (string, error) {
	b, _, err := d.get(ctx, rawURL+escapePath(p), "")
	d.setStatus(err)
	if err != nil {
		return "", err
	}
	body := strings.ReplaceAll(string(b), "\r\n", "\n")
	sha := d.sha(p)
	d.cache.Put(cacheKey(p), []byte(sha+"\n"+body))
	d.learn(p, body, sha)
	return body, nil
}

// learn notes a page's title and slug (or a category's label).
func (d *Docs) learn(p, body, sha string) {
	var m Meta
	if path.Base(p) == "_category_.json" {
		var c struct {
			Label    string
			Position *float64
		}
		if json.Unmarshal([]byte(body), &c) != nil {
			return
		}
		m.Label = c.Label
		if c.Position != nil {
			m.Pos, m.HasPos = *c.Position, true
		}
	} else {
		f, title := PageTitle(body)
		m = Meta{Title: title, Label: f.Label, Slug: f.Slug, Desc: f.Desc, Pos: f.Pos, HasPos: f.HasPos}
	}
	m.Ver = sha
	d.mu.Lock()
	if d.meta[p] != m {
		d.meta[p] = m
		d.dirty = true
		d.rebuild()
		defer d.changed()
	}
	d.mu.Unlock()
}

// Get returns a page: the saved copy when it is current, else a download,
// else an older saved copy.
func (d *Docs) Get(ctx context.Context, p string) (string, error) {
	body, fresh, ok := d.Cached(p)
	if ok && fresh {
		return body, nil
	}
	nb, err := d.Fetch(ctx, p)
	if err == nil {
		d.SaveMeta()
		return nb, nil
	}
	if ok {
		return body, nil
	}
	return "", err
}

// Pages lists every page in navigation order.
func (d *Docs) Pages() []string {
	var out []string
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Page != "" {
			out = append(out, n.Page)
		}
		for _, k := range n.Kids {
			walk(k)
		}
	}
	walk(d.Nav())
	return out
}

// Missing lists the pages (and category files) not saved, or saved in an
// older version. (It goes by the notes in meta.json, not the card.)
func (d *Docs) Missing() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, e := range d.tree.Entries {
		if d.meta[e.Path].Ver != e.SHA {
			out = append(out, e.Path)
		}
	}
	return out
}

// Saved counts the pages on the card, in any version.
func (d *Docs) Saved() (saved, total int) {
	pages := d.Pages()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range pages {
		total++
		if d.meta[p].Ver != "" {
			saved++
		}
	}
	return
}

// ClearSaved deletes the pages saved on the card. Their titles stay, so
// the lists look the same; a page downloads again when it is opened.
func (d *Docs) ClearSaved() {
	d.cache.Prune(0)
	d.mu.Lock()
	for p, m := range d.meta {
		if m.Ver != "" {
			m.Ver = ""
			d.meta[p] = m
			d.dirty = true
		}
	}
	d.mu.Unlock()
	d.SaveMeta()
	d.changed()
}

// SavedSize is how many bytes the saved pages take on the card.
func (d *Docs) SavedSize() int64 {
	ents, err := os.ReadDir(d.cache.Dir())
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && !e.IsDir() {
			n += fi.Size()
		}
	}
	return n
}

// IsSaved reports whether a page is on the card.
func (d *Docs) IsSaved(p string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.meta[p].Ver != ""
}

// DownloadAll saves every page that isn't saved yet (or changed), three at
// a time. progress is called after each one.
func (d *Docs) DownloadAll(ctx context.Context, progress func(done, total int)) (failed int, err error) {
	if err := d.RefreshTree(ctx); err != nil && !d.HasTree() {
		return 0, err
	}
	todo := d.Missing()
	var mu sync.Mutex
	done := 0
	var firstErr error
	var wg sync.WaitGroup
	work := make(chan string)
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				_, err := d.Fetch(ctx, p)
				mu.Lock()
				done++
				if err != nil && ctx.Err() == nil {
					failed++
					if firstErr == nil {
						firstErr = err
					}
				}
				n := done
				mu.Unlock()
				progress(n, len(todo))
			}
		}()
	}
	progress(0, len(todo))
	for _, p := range todo {
		select {
		case work <- p:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	d.SaveMeta()
	if ctx.Err() != nil {
		return failed, ctx.Err()
	}
	return failed, firstErr
}

// Prefetch saves pages in the background (those a list shows), so their
// titles appear and they can be read offline later.
func (d *Docs) Prefetch(paths []string) {
	var todo []string
	d.mu.Lock()
	for _, p := range paths {
		if d.meta[p].Ver != d.shas[p] {
			todo = append(todo, p)
		}
	}
	d.mu.Unlock()
	if len(todo) == 0 || d.Offline() {
		return
	}
	go func() {
		var wg sync.WaitGroup
		for _, p := range todo {
			wg.Add(1)
			d.sem <- struct{}{}
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				d.Fetch(ctx, p)
				cancel()
				<-d.sem
			}()
			if d.Offline() {
				break
			}
		}
		wg.Wait()
		d.SaveMeta()
	}()
}

// ---- navigation ----

var reNum = regexp.MustCompile(`^(\d+)[-_.]`)

// numPrefix is the order a "03-faq" name gives, if any.
func numPrefix(name string) (float64, bool) {
	if m := reNum.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		return float64(n), true
	}
	return 0, false
}

func stripNum(name string) string { return reNum.ReplaceAllString(name, "") }

func stem(p string) string {
	b := path.Base(p)
	return strings.TrimSuffix(b, path.Ext(b))
}

// pretty makes a label from a file or folder name: "03-faq" -> "Faq".
func pretty(name string) string {
	s := stripNum(strings.TrimSuffix(name, path.Ext(name)))
	s = strings.NewReplacer("-", " ", "_", " ").Replace(s)
	s = strings.TrimSpace(s)
	if s == "" {
		return name
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func isIndex(p string) bool {
	switch strings.ToLower(stem(p)) {
	case "index", "readme":
		return true
	}
	dir := path.Dir(p)
	return dir != "." && stripNum(stem(p)) == stripNum(path.Base(dir))
}

// parentDir is p's folder, "" at the top. A path from a damaged tree.json
// that starts with "/" also ends at the top, so building the navigation
// can't loop.
func parentDir(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

const noPos = 1e6

// buildNav makes the navigation from the file list: folders are
// categories, ordered by their _category_.json position or number prefix;
// pages by sidebar_position or number prefix; a folder's index page is the
// category's own page. It also returns the page for each URL slug.
func buildNav(entries []Entry, meta map[string]Meta) (*Node, map[string]string) {
	root := &Node{cat: true}
	dirs := map[string]*Node{"": root}
	var getDir func(dir string) *Node
	getDir = func(dir string) *Node {
		if n, ok := dirs[dir]; ok {
			return n
		}
		parent := getDir(parentDir(dir))
		n := &Node{Dir: dir, name: path.Base(dir), cat: true}
		parent.Kids = append(parent.Kids, n)
		dirs[dir] = n
		return n
	}
	slugs := map[string]string{}
	for _, e := range entries {
		if path.Base(e.Path) == "_category_.json" || !isDoc(e.Path) {
			continue
		}
		dir := parentDir(e.Path)
		parent := getDir(dir)
		slugs[pageSlug(e.Path, meta[e.Path].Slug)] = e.Path
		if dir != "" && isIndex(e.Path) && parent.Page == "" {
			parent.Page = e.Path
			continue
		}
		parent.Kids = append(parent.Kids, &Node{Page: e.Path, name: path.Base(e.Path)})
	}
	var finish func(n *Node)
	finish = func(n *Node) {
		kids := n.Kids[:0]
		for _, k := range n.Kids {
			finish(k)
			if k.cat && len(k.Kids) == 0 && k.Page == "" {
				continue // nothing to read in it (assets)
			}
			kids = append(kids, k)
		}
		n.Kids = kids
		sort.SliceStable(n.Kids, func(i, j int) bool {
			a, b := n.Kids[i], n.Kids[j]
			if a.pos != b.pos {
				return a.pos < b.pos
			}
			return a.name < b.name
		})
		// labels and positions
		n.pos = noPos
		if p, ok := numPrefix(n.name); ok {
			n.pos = p
		}
		if !n.cat {
			m := meta[n.Page]
			n.Label = firstOf(m.Label, m.Title, pretty(n.name))
			if n.Page == "index.md" || n.Page == "index.mdx" {
				// the docs' home page comes first
				n.Label = firstOf(m.Label, m.Title, "Introduction")
				n.pos = -1
			}
			n.Desc = m.Desc
			if m.HasPos {
				n.pos = m.Pos
			}
			return
		}
		c := meta[n.Dir+"/_category_.json"]
		im := meta[n.Page]
		n.Label = firstOf(c.Label, im.Label, im.Title, pretty(n.name))
		n.Desc = im.Desc
		switch {
		case c.HasPos:
			n.pos = c.Pos
		case im.HasPos:
			n.pos = im.Pos
		}
	}
	finish(root)
	return root, slugs
}

func firstOf(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// IsList reports whether the node opens a list rather than a page.
func (n *Node) IsList() bool { return len(n.Kids) > 0 }

// Count is how many pages are under the node.
func (n *Node) Count() int {
	c := 0
	if n.Page != "" {
		c++
	}
	for _, k := range n.Kids {
		c += k.Count()
	}
	return c
}

// defaultSlug is a page's URL path when its front matter sets none:
// the file path without number prefixes or extension, an index page taking
// its folder's.
func defaultSlug(p string) string {
	var segs []string
	for _, s := range strings.Split(parentDir(p), "/") {
		if s != "" {
			segs = append(segs, stripNum(s))
		}
	}
	if !isIndex(p) {
		segs = append(segs, stripNum(stem(p)))
	}
	return "/" + strings.Join(segs, "/")
}

func pageSlug(p, slug string) string {
	if slug == "" {
		return defaultSlug(p)
	}
	if !strings.HasPrefix(slug, "/") {
		dir := defaultSlug(path.Join(parentDir(p), "index.md"))
		slug = path.Join(dir, slug)
	}
	if len(slug) > 1 {
		slug = strings.TrimSuffix(slug, "/")
	}
	return slug
}

// Resolve finds the page (and heading) a link on page from points to.
func (d *Docs) Resolve(from, href string) (string, string, bool) {
	d.mu.Lock()
	slugs := d.slugs
	entries := d.tree.Entries
	fromSlug := pageSlug(from, d.meta[from].Slug)
	d.mu.Unlock()
	return resolve(from, fromSlug, href, slugs, entries)
}

func resolve(from, fromSlug, href string, slugs map[string]string, entries []Entry) (string, string, bool) {
	href = strings.TrimSpace(href)
	if strings.HasPrefix(href, "#") {
		return from, href[1:], true
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", "", false
	}
	p := u.Path
	if u.Scheme != "" {
		if !strings.Contains(u.Host, "onionui.github.io") {
			return "", "", false
		}
		if i := strings.Index(p, "/docs"); i >= 0 {
			p = p[i:]
		}
	}
	if p == "" {
		return from, u.Fragment, u.Fragment != ""
	}
	has := func(f string) bool {
		for _, e := range entries {
			if e.Path == f {
				return true
			}
		}
		return false
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".mdx": // a link to the file itself
		f := path.Clean(path.Join(parentDir(from), p))
		if strings.HasPrefix(p, "/") {
			f = strings.TrimPrefix(path.Clean(p), "/")
			f = strings.TrimPrefix(f, "website/docs/")
		}
		if has(f) {
			return f, u.Fragment, true
		}
		p = strings.TrimSuffix(p, path.Ext(p))
	}
	// a URL: relative to this page's URL, /docs/<slug>
	if !strings.HasPrefix(p, "/") {
		base := "/docs" + fromSlug
		if fromSlug == "/" {
			base = "/docs/"
		}
		dir := path.Dir(base)
		if strings.HasSuffix(base, "/") {
			dir = strings.TrimSuffix(base, "/")
		}
		p = path.Join(dir, p)
	}
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/docs")
	if p == "" {
		p = "/"
	}
	if f, ok := slugs[p]; ok {
		return f, u.Fragment, true
	}
	// Not a known URL (perhaps a slug set in front matter not yet
	// downloaded): match the last part of it with file and folder names.
	last := path.Base(p)
	squash := func(s string) string { return strings.ReplaceAll(strings.ToLower(s), "-", "") }
	for _, try := range []func(string) string{func(s string) string { return s }, squash} {
		for slug, f := range slugs {
			name := stripNum(stem(f))
			if isIndex(f) {
				name = stripNum(path.Base(parentDir(f)))
			}
			if try(name) == try(last) || try(path.Base(slug)) == try(last) {
				return f, u.Fragment, true
			}
		}
	}
	return "", "", false
}

// Breadcrumb names the categories a page is in: "Emulators › Consoles".
func (d *Docs) Breadcrumb(p string) string {
	var trail []string
	var walk func(n *Node, up []string) bool
	walk = func(n *Node, up []string) bool {
		if n.Page == p {
			trail = up
			return true
		}
		if n.cat && n.Label != "" {
			up = append(up, n.Label)
		}
		for _, k := range n.Kids {
			if walk(k, up[:len(up):len(up)]) {
				return true
			}
		}
		return false
	}
	walk(d.Nav(), nil)
	return strings.Join(trail, " › ")
}

// Find returns the category for a folder, or nil.
func (d *Docs) Find(dir string) *Node {
	var walk func(n *Node) *Node
	walk = func(n *Node) *Node {
		if n.Dir == dir {
			return n
		}
		for _, k := range n.Kids {
			if f := walk(k); f != nil {
				return f
			}
		}
		return nil
	}
	return walk(d.Nav())
}

// Title is a page's title for the reader's header.
func (d *Docs) Title(p string) string {
	d.mu.Lock()
	m := d.meta[p]
	d.mu.Unlock()
	if p == "index.md" || p == "index.mdx" {
		return firstOf(m.Title, "Introduction")
	}
	if isIndex(p) {
		return firstOf(m.Title, m.Label, pretty(path.Base(parentDir(p))))
	}
	return firstOf(m.Title, m.Label, pretty(path.Base(p)))
}

// Hit is a search result.
type Hit struct {
	Path, Title, Snippet string
}

// Search finds pages whose title contains q, then saved pages whose text
// does (any case).
func (d *Docs) Search(ctx context.Context, q string) []Hit {
	ql := strings.ToLower(q)
	var titles, texts []Hit
	for _, p := range d.Pages() {
		if ctx.Err() != nil {
			break
		}
		t := d.Title(p)
		if strings.Contains(strings.ToLower(t), ql) {
			titles = append(titles, Hit{Path: p, Title: t, Snippet: firstOf(d.Breadcrumb(p), "Onion OS documentation")})
			continue
		}
		if !d.IsSaved(p) {
			continue
		}
		body, _, ok := d.Cached(p)
		if !ok || !strings.Contains(strings.ToLower(body), ql) {
			continue
		}
		if s := snippet(PlainText(Parse(body).Blocks), ql); s != "" {
			texts = append(texts, Hit{Path: p, Title: t, Snippet: s})
		}
	}
	return append(titles, texts...)
}

// snippet is the text around the first match of ql (lower case), on one
// line, or "" when there is none.
func snippet(text, ql string) string {
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, hardBreak, " ")), " ")
	low := strings.ToLower(text)
	if len(low) != len(text) { // lower-casing changed the length: show it lower-cased
		text = low
	}
	i := strings.Index(low, ql)
	if i < 0 {
		return ""
	}
	start, end := max(0, i-30), min(len(text), i+len(ql)+70)
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	s := text[start:end]
	if start > 0 {
		if j := strings.IndexByte(s, ' '); j >= 0 && j < i-start {
			s = s[j+1:]
		}
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}
