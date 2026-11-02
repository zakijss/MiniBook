package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zakijss/minideck/ui"
)

// A missing settings.json keeps today's behaviour, and the settings save
// and load back.
func TestSettingsFile(t *testing.T) {
	dir := t.TempDir()
	if s := loadSettings(dir); s.OpenLast {
		t.Fatal("opens the last page by default")
	}
	if err := saveSettings(dir, Settings{OpenLast: true}); err != nil {
		t.Fatal(err)
	}
	if s := loadSettings(dir); !s.OpenLast {
		t.Fatal("open_last didn't load back")
	}
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{`), 0o644)
	if s := loadSettings(dir); s.OpenLast {
		t.Fatal("a broken file isn't the defaults")
	}
}

// testDocs is a Docs with two pages in its file list, neither saved. It
// is offline, so nothing downloads in the background.
func testDocs(t *testing.T) *Docs {
	d := NewDocs(t.TempDir(), nil)
	d.tree.Entries = []Entry{{Path: "index.md", SHA: "a"}, {Path: "faq.md", SHA: "b"}}
	d.rebuild()
	d.status = statusOffline
	return d
}

// Start on Last page opens the page last read, if it is still in the docs.
func TestStartPage(t *testing.T) {
	a := &app{d: testDocs(t), st: State{Last: "faq.md"}}
	if p := a.startPage(); p != "" {
		t.Errorf("home by default, got %q", p)
	}
	a.cfg.OpenLast = true
	if p := a.startPage(); p != "faq.md" {
		t.Errorf("last page: got %q", p)
	}
	a.st.Last = "gone.md"
	if p := a.startPage(); p != "" {
		t.Errorf("a page no longer in the docs: got %q", p)
	}
	a.st.Last = ""
	if p := a.startPage(); p != "" {
		t.Errorf("nothing read yet: got %q", p)
	}
}

// Clearing deletes the saved pages but keeps their titles.
func TestClearSaved(t *testing.T) {
	d := testDocs(t)
	d.cache.Put(cacheKey("faq.md"), []byte("b\n# FAQ\n\nText."))
	d.learn("faq.md", "# FAQ\n\nText.", "b")
	if saved, _ := d.Saved(); saved != 1 || d.SavedSize() == 0 {
		t.Fatalf("before: %d saved, %d bytes", saved, d.SavedSize())
	}
	d.ClearSaved()
	if saved, total := d.Saved(); saved != 0 || total != 2 {
		t.Errorf("after: %d of %d saved", saved, total)
	}
	if n := d.SavedSize(); n != 0 {
		t.Errorf("%d bytes left", n)
	}
	if _, _, ok := d.Cached("faq.md"); ok {
		t.Error("page still on the card")
	}
	if got := d.Title("faq.md"); got != "FAQ" {
		t.Errorf("title %q", got)
	}
	var meta map[string]Meta
	b, _ := os.ReadFile(filepath.Join(d.dir, "meta.json"))
	if err := json.Unmarshal(b, &meta); err != nil || meta["faq.md"].Ver != "" || meta["faq.md"].Title != "FAQ" {
		t.Errorf("meta.json: %v %+v", err, meta)
	}
}

func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 KB", 1: "1 KB", 640 << 10: "640 KB", 1468006: "1.4 MB"} {
		if got := formatSize(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}

// START opens Settings from the home list: RIGHT changes the start page,
// which is saved at once, and A on Clear saved pages clears them once
// confirmed.
func TestStartOpensSettings(t *testing.T) {
	scr, _ := lookScreen(t)
	d := testDocs(t)
	d.cache.Put(cacheKey("faq.md"), []byte("b\nText."))
	d.learn("faq.md", "# FAQ\n\nText.", "b")
	dir := t.TempDir()
	a := &app{scr: scr, d: d, dir: dir, poke: make(chan struct{}, 1), st: State{Tops: map[string]int{}},
		input: presses(ui.Start, ui.Right, ui.Down, ui.A, ui.A, ui.B, ui.B)}
	a.home()
	if !a.cfg.OpenLast || !loadSettings(dir).OpenLast {
		t.Errorf("start page not changed and saved: %+v", a.cfg)
	}
	if saved, _ := d.Saved(); saved != 0 {
		t.Errorf("%d pages still saved", saved)
	}
	items := settingsItems(a.cfg, 0, 2, 0)
	if items[0].Detail != "Last page" || items[1].Sub != "No pages are on the card" || items[2].Label != "Appearance" {
		t.Errorf("rows: %+v", items)
	}
}

// TestScreens draws the home list and Settings to PNG files for review. It
// runs only with MINIDECK_GALLERY=DIR set.
func TestScreens(t *testing.T) {
	dir := os.Getenv("MINIDECK_GALLERY")
	if dir == "" {
		t.Skip("set MINIDECK_GALLERY=DIR to draw the screens")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	d := testDocs(t)
	d.cache.Put(cacheKey("faq.md"), []byte("b\n# FAQ\n\nText."))
	d.learn("faq.md", "# FAQ\n\nText.", "b")
	closed := make(chan ui.Event)
	close(closed)
	shot := func(name string, draw func(*app)) {
		t.Setenv("MINIDECK_UI_PNG", filepath.Join(dir, "guide-"+name+".png"))
		scr, err := ui.Open()
		if err != nil {
			t.Fatal(err)
		}
		draw(&app{scr: scr, input: closed, d: d, dir: t.TempDir(), poke: make(chan struct{}, 1),
			st: State{Last: "faq.md", Tops: map[string]int{}}, cfg: Settings{OpenLast: true}})
	}
	shot("home", (*app).home)
	shot("settings", (*app).settings)
}
