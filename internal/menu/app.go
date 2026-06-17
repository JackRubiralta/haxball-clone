package menu

import (
	"context"
	"strconv"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"phootball/internal/audio"
	"phootball/internal/control"
	"phootball/internal/input"
	"phootball/internal/render"
	"phootball/internal/sim"
)

const dt = 1.0 / 60.0

// AppState is the top-level screen the game is showing.
type AppState int

const (
	StateMenu AppState = iota
	StateSettings
	StatePlaying
	StatePaused
	StateResult
)

// App is the local game's state machine. It owns the current match and drives Ebiten's
// Update/Draw. Pausing simply stops stepping the match (no clock, no physics), so a
// resume is seamless.
type App struct {
	ctx         context.Context
	state       AppState
	prevState   AppState
	settings    Settings
	match       *sim.Match
	controllers map[int]control.Controller

	camera *render.Camera
	audio  *audio.Manager

	practice bool
	human    bool
	quit     bool

	// Duo testing mode: one human switches between two players with 1 and 2.
	duo      bool
	duoHuman *input.Human
	activeID int
}

// NewApp creates an app that opens on the main menu.
func NewApp(ctx context.Context, s Settings) *App {
	return &App{ctx: ctx, state: StateMenu, settings: s, camera: render.NewCamera()}
}

// NewPlayingApp creates an app that starts straight into a prepared match (fast-path
// flags such as -solo).
func NewPlayingApp(ctx context.Context, m *sim.Match, controllers map[int]control.Controller, human bool) *App {
	return &App{ctx: ctx, state: StatePlaying, match: m, controllers: controllers, human: human, settings: DefaultSettings(), camera: render.NewCamera()}
}

// NewDuoApp starts the two-player switching tester.
func NewDuoApp(ctx context.Context, m *sim.Match) *App {
	return &App{ctx: ctx, state: StatePlaying, match: m, duo: true, duoHuman: input.NewHuman(), settings: DefaultSettings(), camera: render.NewCamera()}
}

// ConfigureCamera applies a starting follow mode and zoom (from the CLI).
func (a *App) ConfigureCamera(mode string, zoom float64) {
	a.camera.Mode = render.CameraModeFromName(mode)
	if zoom > 0 {
		a.camera.SetZoom(zoom)
	}
}

// ConfigureAudio creates the sound manager (volume 0..1, optionally muted).
func (a *App) ConfigureAudio(volume float64, muted bool) {
	a.audio = audio.New(audio.Settings{Volume: volume, Muted: muted})
}

// afterStep plays this tick's sounds and checks for a finished match.
func (a *App) afterStep() {
	if a.audio != nil {
		a.audio.Dispatch(a.match.DrainEvents())
	}
	if a.match.Finished() {
		a.state = StateResult
	}
}

// Update advances the active screen.
func (a *App) Update() error {
	select {
	case <-a.ctx.Done():
		return ebiten.Termination
	default:
	}
	switch a.state {
	case StatePlaying:
		a.updatePlaying()
	case StateMenu:
		a.screenMenu(updateFrame())
	case StateSettings:
		a.screenSettings(updateFrame())
	case StatePaused:
		a.screenPaused(updateFrame())
	case StateResult:
		a.screenResult(updateFrame())
	}
	if a.quit {
		return ebiten.Termination
	}
	return nil
}

func (a *App) updatePlaying() {
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyP) {
		a.state = StatePaused
		return
	}
	// Camera: mouse wheel zooms (when following the ball), C toggles follow/fit.
	if _, wy := ebiten.Wheel(); wy != 0 {
		a.camera.ZoomBy(1 + 0.12*wy)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyC) {
		a.camera.ToggleFollow()
	}
	if a.duo {
		if inpututil.IsKeyJustPressed(ebiten.KeyDigit1) {
			a.activeID = 0
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyDigit2) {
			a.activeID = 1
		}
		a.match.Step(map[int]sim.Intent{a.activeID: a.duoHuman.Intent(a.match)}, dt)
		a.afterStep()
		return
	}
	inputs := make(map[int]sim.Intent, len(a.controllers))
	for id, c := range a.controllers {
		inputs[id] = c.Intent(a.match)
	}
	a.match.Step(inputs, dt)
	a.afterStep()
}

// Draw renders the active screen.
func (a *App) Draw(screen *ebiten.Image) {
	switch a.state {
	case StatePlaying:
		render.Frame(screen, a.match, a.camera)
	case StatePaused:
		render.Frame(screen, a.match, a.camera)
		a.screenPaused(drawFrame(screen))
	case StateResult:
		render.Frame(screen, a.match, a.camera)
		a.screenResult(drawFrame(screen))
	case StateMenu:
		a.screenMenu(drawFrame(screen))
	case StateSettings:
		a.screenSettings(drawFrame(screen))
	}
}

func (a *App) startMatch(practice, human bool) {
	a.practice, a.human = practice, human
	a.match, a.controllers = a.settings.BuildMatch(practice, human)
	a.state = StatePlaying
}

func (a *App) screenMenu(f frame) {
	if f.draw {
		f.ui.Fill(menuBG)
		f.ui.TextCentered("P H O O T B A L L", render.UIWidth/2, 120)
	}
	const bx, bw = 350.0, 300.0
	y := 220.0
	if f.button("Practice", bx, y, bw, 44) {
		a.startMatch(true, true)
	}
	y += 60
	if f.button("Match vs AI", bx, y, bw, 44) {
		a.startMatch(false, true)
	}
	y += 60
	if f.button("Watch AI", bx, y, bw, 44) {
		a.startMatch(false, false)
	}
	y += 60
	if f.button("Settings", bx, y, bw, 44) {
		a.prevState = StateMenu
		a.state = StateSettings
	}
	y += 60
	if f.button("Quit", bx, y, bw, 44) {
		a.quit = true
	}
}

func (a *App) screenSettings(f frame) {
	if f.draw {
		f.ui.Fill(menuBG)
		f.ui.TextCentered("SETTINGS", render.UIWidth/2, 70)
	}
	s := &a.settings
	y := 140.0
	if dec, inc := f.stepper("Field", s.Field, y); dec || inc {
		s.Field = cycle(fieldPresets, s.Field, dir(inc))
	}
	y += 50
	if dec, inc := f.stepper("Mode", s.Mode, y); dec || inc {
		s.Mode = cycle(modePresets, s.Mode, dir(inc))
	}
	y += 50
	if dec, inc := f.stepper("Team size", strconv.Itoa(s.TeamSize), y); dec || inc {
		s.TeamSize = clampInt(s.TeamSize+dir(inc), 1, 7)
	}
	y += 50
	if dec, inc := f.stepper("Minutes", strconv.Itoa(int(s.Minutes)), y); dec || inc {
		s.Minutes = float64(clampInt(int(s.Minutes)+dir(inc), 1, 30))
	}
	y += 50
	if dec, inc := f.stepper("Win score", strconv.Itoa(s.WinScore), y); dec || inc {
		s.WinScore = clampInt(s.WinScore+dir(inc), 1, 20)
	}
	y += 50
	if f.toggle("Offside (2/3 line)", s.Offside, y) {
		s.Offside = !s.Offside
	}
	y += 50
	if f.toggle("Keeper box (max 1)", s.GKBox, y) {
		s.GKBox = !s.GKBox
	}
	y += 70
	if f.button("Back", 350, y, 300, 44) {
		a.state = a.prevState
	}
}

func (a *App) screenPaused(f frame) {
	if f.draw {
		f.ui.FillRect(0, 0, render.UIWidth, render.UIHeight, overlayBG)
		f.ui.TextCentered("P A U S E D", render.UIWidth/2, 170)
	}
	const bx, bw = 350.0, 300.0
	y := 240.0
	if f.button("Resume", bx, y, bw, 44) {
		a.state = StatePlaying
	}
	y += 58
	if f.button("Settings", bx, y, bw, 44) {
		a.prevState = StatePaused
		a.state = StateSettings
	}
	y += 58
	if !a.duo && f.button("Restart", bx, y, bw, 44) {
		a.startMatch(a.practice, a.human)
	}
	y += 58
	if f.button("Quit to Menu", bx, y, bw, 44) {
		a.match = nil
		a.state = StateMenu
	}
}

func (a *App) screenResult(f frame) {
	if f.draw {
		f.ui.FillRect(0, render.UIHeight-150, render.UIWidth, 150, overlayBG)
	}
	const bx, bw = 350.0, 300.0
	y := render.UIHeight - 128.0
	if f.button("Rematch", bx, y, bw, 42) {
		a.startMatch(a.practice, a.human)
	}
	y += 54
	if f.button("Quit to Menu", bx, y, bw, 42) {
		a.match = nil
		a.state = StateMenu
	}
}

func dir(inc bool) int {
	if inc {
		return 1
	}
	return -1
}
