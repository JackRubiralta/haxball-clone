// Throwaway screenshot harness: renders every UI screen offscreen to /tmp/shots/*.png so the
// menus/HUD can be visually inspected without an interactive session. Run: DISPLAY=:0 go run ./cmd/screenshots
package main

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"

	"github.com/hajimehoshi/ebiten/v2"

	"phootball/internal/menu"
	"phootball/internal/sim"
)

const W, H = 1000, 680

type shot struct {
	name      string
	state     menu.AppState
	tab       int
	withMatch bool
}

type harness struct {
	app   *menu.App
	match *sim.Match
	shots []shot
	i     int
	done  bool
}

func (h *harness) Update() error {
	if h.done {
		return ebiten.Termination
	}
	return nil
}

func (h *harness) Draw(screen *ebiten.Image) {
	if h.i >= len(h.shots) {
		h.done = true
		return
	}
	s := h.shots[h.i]
	h.i++
	off := ebiten.NewImage(W, H)
	var m *sim.Match
	if s.withMatch {
		m = h.match
	}
	h.app.DebugRenderScreen(off, s.state, s.tab, m)
	buf := make([]byte, W*H*4)
	off.ReadPixels(buf)
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	copy(img.Pix, buf)
	f, err := os.Create("/tmp/shots/" + s.name + ".png")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create:", err)
		return
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		return
	}
	fmt.Fprintln(os.Stderr, "wrote", s.name)
}

func (h *harness) Layout(int, int) (int, int) { return W, H }

func main() {
	os.MkdirAll("/tmp/shots", 0o755)
	app := menu.NewApp(context.Background(), menu.DefaultSettings())

	// A match with a populated scoreline / goals / winner for the in-match + result screens.
	m, _ := menu.DefaultSettings().BuildMatch(false, true)
	m.Teams[0].Score = 2
	m.Teams[1].Score = 1
	m.Clock = 125
	// Fake a finished result with a couple of attributed goals for the timeline.
	homeID, awayID := m.Players[1].PlayerID, m.Players[len(m.Players)-1].PlayerID
	m.Goals = []sim.ScoreEvent{
		{Team: sim.SideLeft, Scorer: homeID, HasScorer: true, Time: 34},
		{Team: sim.SideRight, Scorer: awayID, HasScorer: true, Time: 71},
		{Team: sim.SideLeft, Scorer: homeID, HasScorer: true, Time: 88},
	}
	m.State.Phase = sim.PhaseFinished
	m.State.Winner = sim.SideLeft

	h := &harness{
		app:   app,
		match: m,
		shots: []shot{
			{"01_menu", menu.StateMenu, 0, false},
			{"02_setup_teams", menu.StateMatchSetup, 0, false},
			{"03_setup_pitch", menu.StateMatchSetup, 1, false},
			{"04_setup_boxes", menu.StateMatchSetup, 2, false},
			{"05_setup_rules", menu.StateMatchSetup, 3, false},
			{"06_settings", menu.StateSettings, 0, false},
			{"07_playing_hud", menu.StatePlaying, 0, true},
			{"08_paused", menu.StatePaused, 0, true},
			{"09_result", menu.StateResult, 0, true},
		},
	}
	ebiten.SetWindowSize(W, H)
	if err := ebiten.RunGame(h); err != nil {
		fmt.Fprintln(os.Stderr, "RunGame:", err)
		os.Exit(1)
	}
}
