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
	StateMatchSetup
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
	settings    Settings // match options (edited on the pre-match setup screen)
	prefs       AppPrefs // global camera/audio prefs (edited on the settings screen)
	pendingKind matchKind
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
	return &App{ctx: ctx, state: StateMenu, settings: s, prefs: DefaultAppPrefs(), camera: render.NewCamera()}
}

// NewPlayingApp creates an app that starts straight into a prepared match (fast-path
// flags such as -solo).
func NewPlayingApp(ctx context.Context, m *sim.Match, controllers map[int]control.Controller, human bool) *App {
	a := &App{ctx: ctx, state: StatePlaying, match: m, controllers: controllers, human: human,
		settings: DefaultSettings(), prefs: DefaultAppPrefs(), camera: render.NewCamera()}
	a.camera.FocusID = a.humanFocusID()
	return a
}

// NewDuoApp starts the two-player switching tester.
func NewDuoApp(ctx context.Context, m *sim.Match) *App {
	return &App{ctx: ctx, state: StatePlaying, match: m, duo: true, duoHuman: input.NewHuman(),
		settings: DefaultSettings(), prefs: DefaultAppPrefs(), camera: render.NewCamera()}
}

// ConfigureCamera seeds the camera prefs from the CLI and applies them.
func (a *App) ConfigureCamera(mode string, zoom float64) {
	a.prefs.CameraMode = mode
	if zoom > 0 {
		a.prefs.Zoom = zoom
	}
	a.applyPrefs()
}

// ConfigureAudio creates the sound manager from the CLI prefs.
func (a *App) ConfigureAudio(volume float64, muted bool) {
	a.prefs.Volume, a.prefs.Muted = volume, muted
	a.audio = audio.New(audio.Settings{Volume: volume, Muted: muted})
}

// applyPrefs pushes the current camera/audio prefs onto the live camera and mixer.
func (a *App) applyPrefs() {
	a.camera.Mode = render.CameraModeFromName(a.prefs.CameraMode)
	if a.prefs.Zoom > 0 {
		a.camera.SetZoom(a.prefs.Zoom)
	}
	if a.audio != nil {
		a.audio.SetVolume(a.prefs.Volume)
		a.audio.SetMuted(a.prefs.Muted)
	}
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
	case StateMatchSetup:
		a.screenMatchSetup(updateFrame())
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
	// Camera: mouse wheel zooms (when following), C cycles fit/ball/player.
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
		a.match.Step(map[int]sim.Intent{a.activeID: a.duoHuman.Intent(a.match.View())}, dt)
		a.afterStep()
		return
	}
	inputs := make(map[int]sim.Intent, len(a.controllers))
	for id, c := range a.controllers {
		inputs[id] = c.Intent(a.match.View())
	}
	a.match.Step(inputs, dt)
	a.afterStep()
}

// Draw renders the active screen.
func (a *App) Draw(screen *ebiten.Image) {
	switch a.state {
	case StatePlaying:
		render.Frame(screen, a.match, a.camera, dt)
	case StatePaused:
		render.Frame(screen, a.match, a.camera, dt)
		a.screenPaused(drawFrame(screen))
	case StateResult:
		render.Frame(screen, a.match, a.camera, dt)
		a.screenResult(drawFrame(screen))
	case StateMenu:
		a.screenMenu(drawFrame(screen))
	case StateMatchSetup:
		a.screenMatchSetup(drawFrame(screen))
	case StateSettings:
		a.screenSettings(drawFrame(screen))
	}
}

func (a *App) startMatch(practice, human bool) {
	a.practice, a.human = practice, human
	a.match, a.controllers = a.settings.BuildMatch(practice, human)
	a.camera.Reset()
	a.camera.FocusID = a.humanFocusID()
	a.state = StatePlaying
}

// startPending launches the match for the mode chosen on the main menu.
func (a *App) startPending() {
	switch a.pendingKind {
	case kindPractice:
		a.startMatch(true, true)
	case kindWatchAI:
		a.startMatch(false, false)
	default:
		a.startMatch(false, true)
	}
}

// humanFocusID returns the PlayerID of the local human controller (for the player-follow
// camera), or -1 if there is none.
func (a *App) humanFocusID() int {
	for id, c := range a.controllers {
		if _, ok := c.(*input.Human); ok {
			return id
		}
	}
	return -1
}

func (a *App) screenMenu(f frame) {
	if f.draw {
		f.ui.Fill(menuBG)
		f.ui.Title("PHOOTBALL", render.UIWidth/2, 150, 56, accent)
	}
	const bx, bw = 360.0, 280.0
	y := 250.0
	enter := func(label string, kind matchKind) {
		if f.button(label, bx, y, bw, 46) {
			a.pendingKind = kind
			a.state = StateMatchSetup
		}
		y += 62
	}
	enter("Match vs AI", kindVsAI)
	enter("Practice", kindPractice)
	enter("Watch AI", kindWatchAI)
	if f.button("Settings", bx, y, bw, 46) {
		a.prevState = StateMenu
		a.state = StateSettings
	}
	y += 62
	if f.button("Quit", bx, y, bw, 46) {
		a.quit = true
	}
}

func (a *App) screenMatchSetup(f frame) {
	x0, y0, w := f.backdrop("MATCH SETUP")
	colW := (w - 24) / 2
	lx, rx := x0, x0+colW+24
	s := &a.settings
	const rh = 34

	// Left column: teams & pitch.
	ly := y0
	f.sectionHeader("TEAMS & PITCH", lx, ly, colW)
	ly += 26
	if d, i := f.rowStepper("Players /side", strconv.Itoa(s.TeamSize), lx, ly, colW); d || i {
		s.TeamSize = clampInt(s.TeamSize+dir(i), 1, 7)
	}
	ly += rh
	if d, i := f.rowStepper("Field", s.Field, lx, ly, colW); d || i {
		s.Field = cycle(fieldPresets, s.Field, dir(i))
		s.seedSizesFromField()
	}
	ly += rh
	if d, i := f.rowStepper("Goal width", strconv.Itoa(int(s.GoalWidth)), lx, ly, colW); d || i {
		s.GoalWidth = clampF(s.GoalWidth+float64(dir(i))*10, 40, 240)
	}
	ly += rh
	if f.rowToggle("Penalty box", s.PenaltyArea, lx, ly, colW) {
		s.PenaltyArea = !s.PenaltyArea
	}
	ly += rh
	if d, i := f.rowStepper("  Pen. width", strconv.Itoa(int(s.PenaltyWidth)), lx, ly, colW); d || i {
		s.PenaltyWidth = clampF(s.PenaltyWidth+float64(dir(i))*20, 40, 600)
	}
	ly += rh
	if d, i := f.rowStepper("  Pen. max/team", capLabel(s.PenaltyBoxMax), lx, ly, colW); d || i {
		s.PenaltyBoxMax = clampInt(s.PenaltyBoxMax+dir(i), 0, 11)
	}
	ly += rh
	if f.rowToggle("Goal area", s.GoalArea, lx, ly, colW) {
		s.GoalArea = !s.GoalArea
	}
	ly += rh
	if d, i := f.rowStepper("  Area width", strconv.Itoa(int(s.GoalAreaWidth)), lx, ly, colW); d || i {
		s.GoalAreaWidth = clampF(s.GoalAreaWidth+float64(dir(i))*20, 40, 500)
	}
	ly += rh
	if d, i := f.rowStepper("  Area max/team", capLabel(s.GoalAreaMax), lx, ly, colW); d || i {
		s.GoalAreaMax = clampInt(s.GoalAreaMax+dir(i), 0, 11)
	}

	// Right column: rules & match.
	ry := y0
	f.sectionHeader("RULES", rx, ry, colW)
	ry += 26
	if f.rowToggle("Offside", s.Offside, rx, ry, colW) {
		s.Offside = !s.Offside
	}
	ry += rh
	if d, i := f.rowStepper("  Line", strconv.FormatFloat(s.OffsideFrac, 'f', 2, 64), rx, ry, colW); d || i {
		s.OffsideFrac = cycleFrac(s.OffsideFrac, dir(i))
	}
	ry += rh + 10
	f.sectionHeader("MATCH", rx, ry, colW)
	ry += 26
	if d, i := f.rowStepper("Mode", s.Mode, rx, ry, colW); d || i {
		s.Mode = cycle(modePresets, s.Mode, dir(i))
	}
	ry += rh
	if d, i := f.rowStepper("Minutes", strconv.Itoa(int(s.Minutes)), rx, ry, colW); d || i {
		s.Minutes = clampF(s.Minutes+float64(dir(i)), 1, 30)
	}
	ry += rh
	if d, i := f.rowStepper("Win score", strconv.Itoa(s.WinScore), rx, ry, colW); d || i {
		s.WinScore = clampInt(s.WinScore+dir(i), 1, 20)
	}
	ry += rh
	if f.rowToggle("Draw: extra time", s.ExtraTime, rx, ry, colW) {
		s.ExtraTime = !s.ExtraTime
	}
	ry += rh
	if f.rowToggle("Draw: golden goal", s.GoldenGoal, rx, ry, colW) {
		s.GoldenGoal = !s.GoldenGoal
	}
	ry += rh
	if f.rowToggle("Draw: penalties", s.Penalties, rx, ry, colW) {
		s.Penalties = !s.Penalties
	}
	ry += rh
	if d, i := f.rowStepper("AI difficulty", s.Difficulty, rx, ry, colW); d || i {
		s.Difficulty = cycle(difficultyPresets, s.Difficulty, dir(i))
	}

	// Start / Back pinned at the bottom.
	if f.button("Start", x0, y0+440, 200, 40) {
		a.startPending()
	}
	if f.button("Back", x0+w-200, y0+440, 200, 40) {
		a.state = StateMenu
	}
}

func (a *App) screenSettings(f frame) {
	x0, y0, w := f.backdrop("SETTINGS")
	p := &a.prefs
	y := y0 + 10
	const rh = 40

	f.sectionHeader("CAMERA", x0, y, w)
	y += 30
	if d, i := f.rowStepper("Mode", p.CameraMode, x0, y, w); d || i {
		p.CameraMode = cycle(cameraPresets, p.CameraMode, dir(i))
		a.applyPrefs()
	}
	y += rh
	if d, i := f.rowStepper("Zoom", strconv.FormatFloat(p.Zoom, 'f', 1, 64), x0, y, w); d || i {
		p.Zoom = clampF(p.Zoom+float64(dir(i))*0.5, 1, 4)
		a.applyPrefs()
	}
	y += rh + 14
	f.sectionHeader("AUDIO", x0, y, w)
	y += 30
	if d, i := f.rowStepper("Volume", strconv.Itoa(int(p.Volume*100+0.5))+"%", x0, y, w); d || i {
		p.Volume = clampF(p.Volume+float64(dir(i))*0.1, 0, 1)
		a.applyPrefs()
	}
	y += rh
	if f.rowToggle("Mute", p.Muted, x0, y, w) {
		p.Muted = !p.Muted
		a.applyPrefs()
	}
	y += rh + 14
	f.sectionHeader("CONTROLS", x0, y, w)
	y += 26
	if f.draw {
		for _, line := range []string{
			"WASD  move", "Mouse  aim", "Hold left-click  charge shot (release to fire)",
			"Right-click  trap", "Mouse wheel  zoom    C  camera mode", "Esc / P  pause",
		} {
			f.ui.Text(line, x0, y+9)
			y += 26
		}
	}

	if f.button("Back", render.UIWidth/2-100, y0+470, 200, 40) {
		a.state = a.prevState
	}
}

func (a *App) screenPaused(f frame) {
	if f.draw {
		f.ui.FillRect(0, 0, render.UIWidth, render.UIHeight, overlayBG)
		f.ui.Title("PAUSED", render.UIWidth/2, 170, 40, accent)
	}
	const bx, bw = 360.0, 280.0
	y := 250.0
	if f.button("Resume", bx, y, bw, 46) {
		a.state = StatePlaying
	}
	y += 60
	if f.button("Settings", bx, y, bw, 46) {
		a.prevState = StatePaused
		a.state = StateSettings
	}
	y += 60
	if !a.duo && f.button("Restart", bx, y, bw, 46) {
		a.startMatch(a.practice, a.human)
	}
	y += 60
	if f.button("Quit to Menu", bx, y, bw, 46) {
		a.match = nil
		a.state = StateMenu
	}
}

func (a *App) screenResult(f frame) {
	if f.draw {
		f.ui.FillRect(0, render.UIHeight-150, render.UIWidth, 150, overlayBG)
	}
	const bx, bw = 360.0, 280.0
	y := render.UIHeight - 128.0
	if !a.duo && f.button("Rematch", bx, y, bw, 42) {
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

// capLabel shows a per-box cap (0 reads as "off").
func capLabel(n int) string {
	if n <= 0 {
		return "off"
	}
	return strconv.Itoa(n)
}
