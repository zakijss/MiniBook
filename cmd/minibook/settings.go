package main

import (
	"encoding/json"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"

	"github.com/zakijss/minideck/ui"
)

// Settings is settings.json. A missing file or field keeps the defaults:
// MiniBook opens on its home list.
type Settings struct {
	// OpenLast opens the page last read when MiniBook starts, instead of the
	// home list (B then goes to the home list).
	OpenLast bool `json:"open_last,omitempty"`
}

// loadSettings reads settings.json; a missing or unreadable file gives the
// defaults.
func loadSettings(dir string) Settings {
	var s Settings
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("settings: %v", err)
		}
		return s
	}
	if err := json.Unmarshal(b, &s); err != nil {
		log.Printf("settings.json: %v", err)
		return Settings{}
	}
	return s
}

// saveSettings writes settings.json.
func saveSettings(dir string, s Settings) error {
	return writeJSON(filepath.Join(dir, "settings.json"), s)
}

// startPage is the page MiniBook opens at start, or "" for the home list.
func (a *app) startPage() string {
	if a.cfg.OpenLast && a.st.Last != "" && a.d.sha(a.st.Last) != "" {
		return a.st.Last
	}
	return ""
}

// Settings rows, by Value.
const (
	rowStart = iota + 1
	rowClear
	rowLook
)

// settingsItems is the Settings list; saved and total count the pages on
// the card, size is how much room they take.
func settingsItems(s Settings, saved, total int, size int64) []ui.Item {
	start := ui.Item{Label: "Start on", Icon: "list", Detail: "Home", Sub: "The list of pages", Value: rowStart}
	if s.OpenLast {
		start.Detail, start.Sub = "Last page", "The page you were reading"
	}
	wipe := ui.Item{Label: "Clear saved pages", Icon: "cross", Detail: formatSize(size),
		Sub: fmt.Sprintf("%d of %d pages are on the card", saved, total), Value: rowClear}
	if saved == 0 {
		wipe.Sub = "No pages are on the card"
	}
	look := ui.AppearanceItem()
	look.Value = rowLook
	return []ui.Item{start, wipe, look}
}

// formatSize writes a size for Settings: "640 KB", "1.4 MB".
func formatSize(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d KB", (n+1023)>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// settings is the Settings screen: A, left and right change the start
// page; A on Clear saved pages asks first, then deletes them.
func (a *app) settings() {
	var l ui.List
	for {
		saved, total := a.d.Saved()
		l.Items = settingsItems(a.cfg, saved, total, a.d.SavedSize())
		s := a.scr
		top := s.StaticFrame("Settings", a.sub("Left and right change a setting"),
			ui.Hint{Button: "A", Label: "Change"}, ui.Hint{Button: "B", Label: "Back"})
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
		case ui.A, ui.Left, ui.Right:
		case ui.B, ui.Menu, ui.Start:
			return
		default:
			handle(&l, ev)
			continue
		}
		switch l.Items[l.Sel].Value {
		case rowStart:
			old := a.cfg
			a.cfg.OpenLast = !a.cfg.OpenLast
			if err := saveSettings(a.dir, a.cfg); err != nil {
				a.cfg = old
				s.Notice(a.input, "Couldn't save", err.Error()+". Check that the card has free space.")
			}
		case rowClear:
			if ev.Button == ui.A && saved > 0 && s.Confirm(a.input, "Clear saved pages?",
				"MiniBook deletes the pages saved on the card. They download again as you open them, or with Download all for offline.", "Clear") {
				a.d.ClearSaved()
			}
		case rowLook:
			if ev.Button == ui.A {
				a.look()
			}
		}
	}
}
