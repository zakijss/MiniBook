package main

import (
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/zakijss/minideck/ui"
)

// presses is input that presses each button in turn, then ends.
func presses(bs ...ui.Button) <-chan ui.Event {
	ch := make(chan ui.Event, len(bs))
	for _, b := range bs {
		ch <- ui.Event{Button: b, Pressed: true}
	}
	close(ch)
	return ch
}

// lookScreen opens a screen that draws nowhere, with the shared look saved
// to a temporary file. The look in use is put back after the test.
func lookScreen(t *testing.T) (*ui.Screen, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "appearance.json")
	t.Setenv("MINIDECK_UI_NULL", "1")
	t.Setenv("MINIDECK_APPEARANCE", path)
	scr, err := ui.Open()
	if err != nil {
		t.Fatal(err)
	}
	start := ui.CurrentAppearance()
	t.Cleanup(func() { scr.SetAppearance(start) })
	return scr, path
}

// wantLookChanged checks that Appearance was opened: RIGHT on its first
// row moved to the next theme, and B saved it.
func wantLookChanged(t *testing.T, start ui.Appearance, path string) {
	t.Helper()
	if ui.CurrentAppearance().Theme == start.Theme {
		t.Errorf("theme still %q: Appearance didn't open", start.Theme)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("look not saved: %v", err)
	}
}

// SELECT opens Appearance, and a change of look drops the laid-out pages,
// whose colours belong to the old theme.
func TestLookRelaysOut(t *testing.T) {
	scr, path := lookScreen(t)
	start := ui.CurrentAppearance()
	a := &app{scr: scr, input: presses(ui.Right, ui.B), pages: []*Page{Layout(Parse("# Title\n\nText."), textW)}}
	a.look()
	wantLookChanged(t, start, path)
	if len(a.pages) != 0 {
		t.Errorf("%d pages kept from the old theme", len(a.pages))
	}
	pg := Layout(Parse("Text."), textW)
	if c := pg.Lines[0].Runs[0].Color; c != ui.Text {
		t.Errorf("text laid out in %v, want the new theme's %v", c, ui.Text)
	}
}

// luminance is the WCAG relative luminance of c.
func luminance(c color.RGBA) float64 {
	f := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*f(c.R) + 0.7152*f(c.G) + 0.0722*f(c.B)
}

func contrast(a, b color.RGBA) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// The link and callout colours are the same in every theme: they must
// still read on every theme's page card. Links are text (4.5:1). Callout
// titles are bold, on their own tinted box; important's purple and
// danger's red are the weakest, close to 3:1, so this keeps them from
// getting any fainter.
func TestFixedColoursReadInEveryTheme(t *testing.T) {
	scr, _ := lookScreen(t)
	for _, th := range ui.Themes {
		scr.SetAppearance(ui.Appearance{Theme: th.ID, Background: "plain"})
		r := image.Rect(cardX0, 100, cardX1, 300)
		scr.Panel(r, 18)
		card := scr.RGBAAt((r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2)
		if c := contrast(linkColor, card); c < 4.5 {
			t.Errorf("%s: links %.2f:1 on the card", th.Name, c)
		}
		for kind := range noteColors {
			col := noteColor(kind)
			if c := contrast(col, ui.Mix(card, col, 0.13)); c < 3 {
				t.Errorf("%s: %s callout title %.2f:1", th.Name, kind, c)
			}
		}
	}
}
