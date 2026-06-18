package menu

import (
	"context"
	"image/color"
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
	match       *sim.Match
	controllers map[int]control.Controller

	// Match-setup IA: the selected tab and one scroll pane per tab (offset survives
	// across frames; the wheel is read in the update pass only).
	setupTab       int
	setupScroll    [4]scrollState
	settingsScroll scrollState

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
	// Camera: C cycles fit/ball/player. Mouse-wheel zoom is intentionally NOT handled here --
	// the wheel only adjusts zoom in Settings, so scrolling can't disturb the camera mid-match.
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

// DebugRenderScreen renders a single screen to dst for offscreen screenshot tooling (not used
// in normal play). state selects the screen, tab the Match Setup tab; m (if non-nil) backs the
// in-match screens (Playing/Paused/Result). It runs only the draw pass.
func (a *App) DebugRenderScreen(dst *ebiten.Image, state AppState, tab int, m *sim.Match) {
	a.setupTab = tab
	if m != nil {
		a.match = m
	}
	a.state = state
	a.Draw(dst)
}

// DebugScrollSetup pre-sets a Match Setup tab's scroll offset (screenshot tooling only).
func (a *App) DebugScrollSetup(tab int, offset float64) { a.setupScroll[tab].offset = offset }

// DebugSetRules forces draw-resolution settings (screenshot tooling only) so the rules summary
// can be captured for a given combo.
func (a *App) DebugSetRules(winByTime, extraTime, golden, goldenCapped, penalties bool, extraMin float64) {
	a.settings.WinByTime = winByTime
	a.settings.ExtraTime = extraTime
	a.settings.GoldenGoal = golden
	a.settings.GoldenGoalCapped = goldenCapped
	a.settings.Penalties = penalties
	a.settings.ExtraMinutes = extraMin
	a.settings.PenaltyBestOf = 5
	a.settings.ClampDependents()
}

func (a *App) startMatch(practice, human bool) {
	a.practice, a.human = practice, human
	a.match, a.controllers = a.settings.BuildMatch(practice, human)
	a.camera.Reset()
	a.camera.FocusID = a.humanFocusID()
	a.state = StatePlaying
}

// startSetup launches the match configured on the Match Setup screen. The mode is derived
// from the per-team control: a human plays whenever either team is set to Human.
func (a *App) startSetup() {
	a.startMatch(false, a.settings.Teams[teamHome].Human || a.settings.Teams[teamAway].Human)
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
		f.ui.Fill(theme.BG)
		f.ui.Title("PHOOTBALL", render.UIWidth/2, 150, theme.Title, theme.Accent)
	}
	bw, bh := 280.0, theme.BtnH
	bx := render.UIWidth/2 - bw/2
	y := 260.0
	if f.button("Play Match", bx, y, bw, bh) {
		a.state = StateMatchSetup
	}
	y += bh + 18
	if f.button("Settings", bx, y, bw, bh) {
		a.prevState = StateMenu
		a.state = StateSettings
	}
	y += bh + 18
	if f.button("Quit", bx, y, bw, bh) {
		a.quit = true
	}
}

// setupTabs are the Match Setup detail panes, in tab-rail order.
var setupTabs = []string{"Teams & Control", "Pitch & Goals", "Boxes", "Match Rules"}

func (a *App) screenMatchSetup(f frame) {
	// Page chrome: backdrop + title, a left tab rail, a scrollable detail pane, and a
	// pinned action bar (Back left, Start right) with a validation banner.
	const px, py, pw, ph = 60.0, 56.0, 880.0, 568.0
	const railW = 196.0
	const barH = 56.0
	pad := theme.PanelPad
	if f.draw {
		f.ui.Fill(theme.BG)
		f.ui.Panel(px, py, pw, ph, theme.Panel, theme.Edge)
		f.ui.Title("MATCH SETUP", render.UIWidth/2, py+30, theme.H1, theme.Accent)
	}
	contentTop := py + 72
	railX := px + pad
	railY := contentTop
	a.setupTab = f.tabRail(setupTabs, a.setupTab, railX, railY, railW, theme.BtnH)

	// Detail pane to the right of the rail.
	paneX := railX + railW + pad
	paneY := contentTop
	paneW := px + pw - pad - paneX
	barY := py + ph - pad - barH
	paneH := barY - paneY - 12

	// Read the wheel for the active tab in the update pass only.
	if !f.draw {
		if _, wy := ebiten.Wheel(); wy != 0 {
			a.setupScroll[a.setupTab].Scroll(wy)
		}
	}
	sc := &a.setupScroll[a.setupTab]
	top, cf := f.beginScroll(sc, paneX, paneY, paneW, paneH)
	col := newCol(paneX, top, paneW-12) // -12 leaves room for the scrollbar
	switch a.setupTab {
	case 0:
		a.setupTeams(cf, &col)
	case 1:
		a.setupPitch(cf, &col)
	case 2:
		a.setupBoxes(cf, &col)
	case 3:
		a.setupRules(cf, &col)
	}
	f.endScroll(sc, paneX, paneY, paneW, paneH, col.cursorY())

	// Action bar: Back (left), validation banner (centre), Start (right, gated).
	const abw = 180.0
	if f.button("Back", railX, barY, abw, barH) {
		a.state = StateMenu
	}
	err := a.settings.Validate()
	if f.draw && err != nil {
		f.ui.TextCenteredS(err.Error(), render.UIWidth/2, barY+barH/2, theme.Small, theme.Bad)
	}
	startX := px + pw - pad - abw
	if err == nil {
		if f.button("Start", startX, barY, abw, barH) {
			a.startSetup()
		}
	} else if f.draw {
		// Disabled Start: drawn dim, not clickable.
		f.ui.FillRect(startX, barY, abw, barH, theme.BtnBG)
		f.ui.StrokeRect(startX, barY, abw, barH, 2, theme.Edge)
		f.ui.TextCenteredS("Start", startX+abw/2, barY+barH/2, theme.Body, theme.TextDim)
	}
}

// setupTeams draws the two mirrored Blue/Red team columns: Human/AI control, the human's
// slot (human team only), the AI difficulty (AI team only), and the roster size.
func (a *App) setupTeams(f frame, col *colLayout) {
	s := &a.settings
	half := (col.w - theme.PanelPad) / 2
	topY := col.cursorY()
	names := [2]string{"BLUE (home)", "RED (away)"}
	xs := [2]float64{col.x, col.x + half + theme.PanelPad}
	maxY := topY
	for ti := range s.Teams {
		c := newCol(xs[ti], topY, half)
		f.sectionHeader(names[ti], c.x, c.header(1), c.w)
		tc := &s.Teams[ti]
		ctlSel := 0
		if !tc.Human {
			ctlSel = 1
		}
		if sel := f.segmented("Control", controlPresets, ctlSel, c.x, c.row(), c.w); sel != ctlSel {
			tc.Human = sel == 0
			s.ClampDependents()
		}
		if d, i := f.rowStepper("Size", strconv.Itoa(tc.Size), c.x, c.row(), c.w); d || i {
			tc.Size = clampInt(tc.Size+dir(i), 1, 7)
			s.ClampDependents()
		}
		if tc.Human {
			if d, i := f.rowStepper("Human slot", strconv.Itoa(tc.HumanSlot), c.x, c.row(), c.w); d || i {
				tc.HumanSlot = clampInt(tc.HumanSlot+dir(i), 1, tc.Size)
				s.ClampDependents()
			}
		} else {
			diff := indexOf(difficultyPresets, tc.Difficulty)
			if sel := f.segmented("AI", difficultyPresets, diff, c.x, c.row(), c.w); sel != diff {
				tc.Difficulty = difficultyPresets[sel]
			}
		}
		if c.cursorY() > maxY {
			maxY = c.cursorY()
		}
	}
	col.y = maxY
	if f.draw && s.Teams[teamHome].Human && s.Teams[teamAway].Human {
		f.ui.TextS("Note: one local keyboard drives Blue; Red's human reverts to AI.",
			col.x, col.row()+theme.RowH/2, theme.Small, theme.TextDim)
	} else if f.draw && !s.Teams[teamHome].Human && !s.Teams[teamAway].Human {
		f.ui.TextS("Both AI: this is a watch match.", col.x, col.row()+theme.RowH/2, theme.Small, theme.TextDim)
	}
}

// setupPitch draws the pitch/goal dimension steppers as the primary direct controls,
// preceded by quick-fill preset BUTTONS. Each button populates every dimension explicitly
// from its config geometry (so nothing stays bundled in a mode); the steppers below are
// always editable and apply the relational clamps after each edit.
func (a *App) setupPitch(f frame, col *colLayout) {
	s := &a.settings
	f.sectionHeader("PITCH & GOALS", col.x, col.header(1), col.w)

	// Quick-fill: three buttons that fully populate the dimensions from a preset.
	if f.draw {
		f.ui.TextS("Quick-fill from preset:", col.x, col.row()+theme.RowH/2, theme.Body, theme.Text)
	} else {
		col.row()
	}
	labels := [3]string{"Standard", "Small", "Large"}
	by := col.row()
	bw := (col.w - 2*theme.PanelPad) / 3
	bh := theme.RowH - 10
	for i, name := range fieldPresets {
		if f.button(labels[i], col.x+float64(i)*(bw+theme.PanelPad), by, bw, bh) {
			s.ApplyPreset(name)
		}
	}

	if d, i := f.rowStepper("Pitch length", dimLabel(s.PlayWidth), col.x, col.row(), col.w); d || i {
		s.PlayWidth = stepDim(s.PlayWidth, dir(i), 40, 400, 2400)
		s.ClampDependents()
	}
	if d, i := f.rowStepper("Pitch width", dimLabel(s.PlayHeight), col.x, col.row(), col.w); d || i {
		s.PlayHeight = stepDim(s.PlayHeight, dir(i), 40, 240, 1600)
		s.ClampDependents()
	}
	if d, i := f.rowStepper("Goal width", strconv.Itoa(int(s.GoalWidth)), col.x, col.row(), col.w); d || i {
		s.GoalWidth = clampF(s.GoalWidth+float64(dir(i))*10, 40, 240)
		s.ClampDependents()
	}
	if d, i := f.rowStepper("Goal depth", dimLabel(s.GoalDepth), col.x, col.row(), col.w); d || i {
		s.GoalDepth = stepDim(s.GoalDepth, dir(i), 5, 10, 80)
		s.ClampDependents()
	}
	if f.draw {
		f.ui.TextS("\"auto\" = derive from the base; quick-fill makes every value explicit.",
			col.x, col.row()+theme.RowH/2, theme.Small, theme.TextDim)
	}
}

// setupBoxes draws the penalty area and goal area: enable, dimensions, caps, keeper-only.
// Sub-rows grey out when a box is disabled, and numeric caps grey when keeper-only is set.
func (a *App) setupBoxes(f frame, col *colLayout) {
	s := &a.settings
	f.sectionHeader("PENALTY AREA", col.x, col.header(1), col.w)
	if f.rowToggle("Enabled", s.PenaltyArea, col.x, col.row(), col.w) {
		s.PenaltyArea = !s.PenaltyArea
		s.ClampDependents()
	}
	if s.PenaltyArea {
		if d, i := f.rowStepper("Width", strconv.Itoa(int(s.PenaltyWidth)), col.x, col.row(), col.w); d || i {
			s.PenaltyWidth = clampF(s.PenaltyWidth+float64(dir(i))*20, 40, 800)
			s.ClampDependents()
		}
		if d, i := f.rowStepper("Depth", strconv.Itoa(int(s.PenaltyDepth)), col.x, col.row(), col.w); d || i {
			s.PenaltyDepth = clampF(s.PenaltyDepth+float64(dir(i))*10, 20, 600)
			s.ClampDependents()
		}
		if d, i := f.rowStepper("Max defenders", capLabel(s.PenaltyBoxMax), col.x, col.row(), col.w); d || i {
			s.PenaltyBoxMax = clampInt(s.PenaltyBoxMax+dir(i), 0, 11)
		}
		if d, i := f.rowStepper("Max attackers", capLabel(s.PenaltyBoxMaxOpp), col.x, col.row(), col.w); d || i {
			s.PenaltyBoxMaxOpp = clampInt(s.PenaltyBoxMaxOpp+dir(i), 0, 11)
		}
	} else {
		f.disabledRow("(penalty area disabled)", col.x, col.row(), col.w)
	}

	col.gapRow(0.4)
	f.sectionHeader("GOAL AREA", col.x, col.header(1), col.w)
	if f.rowToggle("Enabled", s.GoalArea, col.x, col.row(), col.w) {
		s.GoalArea = !s.GoalArea
		s.ClampDependents()
	}
	if s.GoalArea {
		if d, i := f.rowStepper("Width", strconv.Itoa(int(s.GoalAreaWidth)), col.x, col.row(), col.w); d || i {
			s.GoalAreaWidth = clampF(s.GoalAreaWidth+float64(dir(i))*20, 40, 700)
			s.ClampDependents()
		}
		if d, i := f.rowStepper("Depth", strconv.Itoa(int(s.GoalAreaDepth)), col.x, col.row(), col.w); d || i {
			s.GoalAreaDepth = clampF(s.GoalAreaDepth+float64(dir(i))*10, 20, 500)
			s.ClampDependents()
		}
		if f.rowToggle("Keeper-only", s.GoalAreaKeeperOnly, col.x, col.row(), col.w) {
			s.GoalAreaKeeperOnly = !s.GoalAreaKeeperOnly
		}
		if s.GoalAreaKeeperOnly {
			f.disabledRow("Max defenders / attackers: keeper-only", col.x, col.row(), col.w)
		} else {
			if d, i := f.rowStepper("Max defenders", capLabel(s.GoalAreaMax), col.x, col.row(), col.w); d || i {
				s.GoalAreaMax = clampInt(s.GoalAreaMax+dir(i), 0, 11)
			}
			if d, i := f.rowStepper("Max attackers", capLabel(s.GoalAreaMaxOpp), col.x, col.row(), col.w); d || i {
				s.GoalAreaMaxOpp = clampInt(s.GoalAreaMaxOpp+dir(i), 0, 11)
			}
		}
	} else {
		f.disabledRow("(goal area disabled)", col.x, col.row(), col.w)
	}
}

// setupRules draws the orthogonal win/draw conditions: a "win by goals" target and/or a
// "time limit", a derived summary, then the on-a-draw resolution (extra time -- with a
// golden-goal sudden-death modifier -- and a penalty shootout).
func (a *App) setupRules(f frame, col *colLayout) {
	s := &a.settings
	f.sectionHeader("WIN CONDITION", col.x, col.header(1), col.w)
	if f.rowToggle("Win by goals", s.WinByGoals, col.x, col.row(), col.w) {
		s.WinByGoals = !s.WinByGoals
	}
	if s.WinByGoals {
		if d, i := f.rowStepper("Goals to win", strconv.Itoa(s.WinScore), col.x, col.row(), col.w); d || i {
			s.WinScore = clampInt(s.WinScore+dir(i), 1, 20)
		}
	}
	if f.rowToggle("Time limit", s.WinByTime, col.x, col.row(), col.w) {
		s.WinByTime = !s.WinByTime
	}
	if s.WinByTime {
		if d, i := f.rowStepper("Minutes", strconv.Itoa(int(s.Minutes)), col.x, col.row(), col.w); d || i {
			s.Minutes = clampF(s.Minutes+float64(dir(i)), 1, 30)
		}
	}
	f.disabledRow(winSummary(s), col.x, col.row(), col.w)

	col.gapRow(0.3)
	f.sectionHeader("— ON A DRAW —", col.x, col.header(1), col.w)
	if f.rowToggle("Extra time", s.ExtraTime, col.x, col.row(), col.w) {
		s.ExtraTime = !s.ExtraTime
		s.ClampDependents()
	}
	if s.ExtraTime {
		if f.rowToggle("Golden goal (next goal wins)", s.GoldenGoal, col.x, col.row(), col.w) {
			s.GoldenGoal = !s.GoldenGoal
			s.ClampDependents()
		}
		if s.GoldenGoal {
			// Golden goal: optionally cap the sudden-death period; the cap reuses Extra minutes.
			if f.rowToggle("Time limit", s.GoldenGoalCapped, col.x, col.row(), col.w) {
				s.GoldenGoalCapped = !s.GoldenGoalCapped
			}
			if s.GoldenGoalCapped {
				if d, i := f.rowStepper("Minutes", strconv.Itoa(int(s.ExtraMinutes)), col.x, col.row(), col.w); d || i {
					s.ExtraMinutes = clampF(s.ExtraMinutes+float64(dir(i)), 1, 30)
				}
			}
		} else {
			if d, i := f.rowStepper("Extra minutes", strconv.Itoa(int(s.ExtraMinutes)), col.x, col.row(), col.w); d || i {
				s.ExtraMinutes = clampF(s.ExtraMinutes+float64(dir(i)), 1, 30)
			}
		}
	}
	if f.rowToggle("Penalty shootout", s.Penalties, col.x, col.row(), col.w) {
		s.Penalties = !s.Penalties
		s.ClampDependents()
	}
	if s.Penalties {
		hint := "(direct)"
		if s.ExtraTime {
			hint = "(after extra time)"
		}
		if d, i := f.rowStepper("Penalty kicks "+hint, penBestLabel(s.PenaltyBestOf), col.x, col.row(), col.w); d || i {
			s.PenaltyBestOf = clampInt(s.PenaltyBestOf+dir(i), 1, 11)
		}
	}
	// Derived one-line summary of how a level match resolves (mirrors MatchSetup.Ruleset's
	// OnDraw chain). With neither extra time nor penalties enabled it simply reads "A draw
	// stands." -- so the old explicit toggle for that was redundant and is gone.
	f.disabledRow(drawSummary(s), col.x, col.row(), col.w)
}

// winSummary describes the configured win condition in a single line for the Match Rules
// tab, mirroring MatchSetup.Ruleset's mapping.
func winSummary(s *Settings) string {
	switch {
	case s.WinByGoals && s.WinByTime:
		return "First to " + strconv.Itoa(s.WinScore) + " goals, or lead after " + strconv.Itoa(int(s.Minutes)) + " min."
	case s.WinByGoals:
		return "First to " + strconv.Itoa(s.WinScore) + " goals (no clock)."
	case s.WinByTime:
		return "Whoever leads after " + strconv.Itoa(int(s.Minutes)) + " min wins."
	default:
		return "Friendly: the match never ends."
	}
}

// drawSummary describes, in one well-phrased line, how a level match is resolved -- mirroring
// MatchSetup.Ruleset's OnDraw chain (extra time / golden goal, then a shootout, else the draw
// stands). Only a timed match can finish level, so it says so otherwise.
func drawSummary(s *Settings) string {
	if !s.WinByTime {
		return "Only a timed match can end in a draw."
	}
	// First stage: extra time (fixed) or golden goal (uncapped = decides it; capped = may end level).
	seg := ""
	switch {
	case s.ExtraTime && s.GoldenGoal && !s.GoldenGoalCapped:
		return "Golden goal: first goal wins."
	case s.ExtraTime && s.GoldenGoal:
		seg = strconv.Itoa(int(s.ExtraMinutes)) + " min golden goal"
	case s.ExtraTime:
		seg = strconv.Itoa(int(s.ExtraMinutes)) + " min extra time"
	}
	// Terminal outcome after that stage.
	switch {
	case seg != "" && s.Penalties:
		return seg + ", then penalties."
	case seg != "":
		return seg + ", then a draw stands."
	case s.Penalties:
		return "Straight to penalties."
	default:
		return "A draw stands."
	}
}

func (a *App) screenSettings(f frame) {
	const px, py, pw, ph = 120.0, 56.0, 760.0, 568.0
	const barH = 56.0
	pad := theme.PanelPad
	if f.draw {
		f.ui.Fill(theme.BG)
		f.ui.Panel(px, py, pw, ph, theme.Panel, theme.Edge)
		f.ui.Title("SETTINGS", render.UIWidth/2, py+30, theme.H1, theme.Accent)
	}
	p := &a.prefs
	paneX := px + pad
	paneY := py + 72
	paneW := pw - 2*pad
	barY := py + ph - pad - barH
	paneH := barY - paneY - 12

	// The mouse wheel tunes zoom only when hovering the camera rows is awkward in a scroll
	// pane, so route the wheel to the scroll pane and keep zoom on its stepper.
	if !f.draw {
		if _, wy := ebiten.Wheel(); wy != 0 {
			a.settingsScroll.Scroll(wy)
		}
	}
	top, cf := f.beginScroll(&a.settingsScroll, paneX, paneY, paneW, paneH)
	col := newCol(paneX, top, paneW-12)

	cf.sectionHeader("CAMERA", col.x, col.header(1), col.w)
	if d, i := cf.rowStepper("Mode", p.CameraMode, col.x, col.row(), col.w); d || i {
		p.CameraMode = cycle(cameraPresets, p.CameraMode, dir(i))
		a.applyPrefs()
	}
	if d, i := cf.rowStepper("Zoom", strconv.FormatFloat(p.Zoom, 'f', 2, 64)+"x", col.x, col.row(), col.w); d || i {
		p.Zoom = clampF(p.Zoom+float64(dir(i))*0.25, 1, 4)
		a.applyPrefs()
	}
	col.gapRow(0.3)
	cf.sectionHeader("AUDIO", col.x, col.header(1), col.w)
	if d, i := cf.rowStepper("Volume", strconv.Itoa(int(p.Volume*100+0.5))+"%", col.x, col.row(), col.w); d || i {
		p.Volume = clampF(p.Volume+float64(dir(i))*0.1, 0, 1)
		a.applyPrefs()
	}
	if cf.rowToggle("Mute", p.Muted, col.x, col.row(), col.w) {
		p.Muted = !p.Muted
		a.applyPrefs()
	}
	col.gapRow(0.3)
	cf.sectionHeader("CONTROLS", col.x, col.header(1), col.w)
	for _, line := range []string{
		"WASD  move", "Mouse  aim", "Hold left-click  charge shot (release to fire)",
		"Right-click  trap", "Middle-click  push", "C  camera mode", "Esc / P  pause",
	} {
		y := col.row()
		if cf.draw {
			cf.ui.TextS(line, col.x, y+theme.RowH/2, theme.Body, theme.Text)
		}
	}
	f.endScroll(&a.settingsScroll, paneX, paneY, paneW, paneH, col.cursorY())

	if f.button("Back", render.UIWidth/2-90, barY, 180, barH) {
		a.state = a.prevState
	}
}

func (a *App) screenPaused(f frame) {
	if f.draw {
		f.ui.FillRect(0, 0, render.UIWidth, render.UIHeight, theme.Overlay)
		f.ui.Title("PAUSED", render.UIWidth/2, 170, theme.H1, theme.Accent)
	}
	bw, bh := 280.0, theme.BtnH
	bx := render.UIWidth/2 - bw/2
	y := 250.0
	if f.button("Resume", bx, y, bw, bh) {
		a.state = StatePlaying
	}
	y += bh + 16
	if f.button("Settings", bx, y, bw, bh) {
		a.prevState = StatePaused
		a.state = StateSettings
	}
	y += bh + 16
	if !a.duo && f.button("Restart", bx, y, bw, bh) {
		a.startMatch(a.practice, a.human)
	}
	y += bh + 16
	if f.button("Quit to Menu", bx, y, bw, bh) {
		a.match = nil
		a.state = StateMenu
	}
}

// resultScrollState persists the goal-timeline scroll offset across frames.
var resultScroll scrollState

// resultOverlay is the near-opaque backdrop behind the result panel: dark enough that the
// in-match HUD card behind it does not bleed through above the panel (the bug where the
// bright scoreboard showed over the top edge).
var resultOverlay = color.RGBA{6, 12, 10, 245}

func (a *App) screenResult(f frame) {
	r := buildResult(a.match)

	// Full-time panel over the dimmed final frame. The overlay is drawn nearly opaque so the
	// in-match HUD card behind it cannot bleed through above the panel, and the panel itself
	// starts near the very top so the result reads cleanly over where the HUD sat.
	const px, py, pw, ph = 110.0, 24.0, 780.0, 616.0
	const barH = 52.0
	pad := theme.PanelPad
	if f.draw {
		f.ui.FillRect(0, 0, render.UIWidth, render.UIHeight, resultOverlay)
		f.ui.Panel(px, py, pw, ph, theme.Panel, theme.Edge)
	}
	innerX := px + pad
	innerW := pw - 2*pad

	// --- Header: team chips flanking the big scoreline, then the winner line + tag. ---
	headTop := py + 24
	a.drawResultHeader(f, r, innerX, headTop, innerW)

	// Action bar (pinned bottom).
	barY := py + ph - pad - barH
	bw := 220.0
	gap := 24.0
	bx := innerX + (innerW-(2*bw+gap))/2
	if !a.duo && f.button("Rematch", bx, barY, bw, barH) {
		a.startMatch(a.practice, a.human)
	}
	if f.button("Quit to Menu", bx+bw+gap, barY, bw, barH) {
		a.match = nil
		a.state = StateMenu
	}

	// --- Body: a scroll pane holding the goal timeline, shootout block, and stats. ---
	bodyTop := headTop + 132
	bodyBot := barY - 16
	paneH := bodyBot - bodyTop

	if !f.draw {
		if _, wy := ebiten.Wheel(); wy != 0 {
			resultScroll.Scroll(wy)
		}
	}
	top, cf := f.beginScroll(&resultScroll, innerX, bodyTop, innerW, paneH)
	col := newCol(innerX, top, innerW-12) // -12 leaves room for the scrollbar
	a.drawResultTimeline(cf, r, &col)
	if r.HasShootout {
		col.gapRow(0.4)
		a.drawResultShootout(cf, r, &col)
	}
	col.gapRow(0.4)
	a.drawResultStats(cf, &col)
	f.endScroll(&resultScroll, innerX, bodyTop, innerW, paneH, col.cursorY())
}

// drawResultHeader renders the two team chips, the big final scoreline between them, the
// winner line with a trophy, and the context tag.
func (a *App) drawResultHeader(f frame, r ResultModel, x, y, w float64) {
	if !f.draw {
		return
	}
	cx := x + w/2
	chipW := 220.0
	const swatch = 20.0
	sr := swatch / 2

	// Big centred scoreline.
	score := strconv.Itoa(r.Teams[0].Score) + " - " + strconv.Itoa(r.Teams[1].Score)
	f.ui.TextCenteredS(score, cx, y+30, 52, theme.Text)

	// Left (home) chip: a team-colour swatch then the name (consistent with the HUD card).
	render.TeamSwatch(f.screen, x+sr, y+24, swatch, r.Teams[0].Color)
	f.ui.TextS(fitMenu(f, r.Teams[0].Name, chipW-swatch-12, theme.Body), x+swatch+10, y+24, theme.Body, r.Teams[0].Color)

	// Right (away) chip: the name then a swatch, mirrored.
	rxEnd := x + w
	render.TeamSwatch(f.screen, rxEnd-sr, y+24, swatch, r.Teams[1].Color)
	f.ui.TextRightS(fitMenu(f, r.Teams[1].Name, chipW-swatch-12, theme.Body), rxEnd-swatch-10, y+24, theme.Body, r.Teams[1].Color)

	// Winner line with a trophy (a trophy only for a decided result), then the context tag.
	winY := y + 76
	line := r.WinnerLine
	tw := f.ui.MeasureUI(line, theme.Section)
	if r.WinnerIdx >= 0 {
		render.IconTrophy(f.screen, cx-tw/2-18, winY, 24, theme.Accent)
	}
	f.ui.TextCenteredS(line, cx, winY, theme.Section, r.WinnerTint)
	if r.ContextTag != "" {
		f.ui.TextCenteredS(r.ContextTag, cx, winY+24, theme.Small, theme.TextDim)
	}
}

// drawResultTimeline renders the chronological goal list, team-coloured per row.
func (a *App) drawResultTimeline(f frame, r ResultModel, col *colLayout) {
	f.sectionHeader("GOALS", col.x, col.header(1), col.w)
	if len(r.Goals) == 0 {
		y := col.row()
		if f.draw {
			f.ui.TextS("No goals.", col.x, y+theme.RowH/2, theme.Body, theme.TextDim)
		}
		return
	}
	for _, g := range r.Goals {
		y := col.row()
		if !f.draw {
			continue
		}
		midY := y + theme.RowH/2
		// Time, then a team-colour dot, scorer, and detail.
		f.ui.TextS(g.Time, col.x, midY, theme.Body, theme.TextDim)
		dotX := col.x + 64
		f.ui.FillRect(dotX, midY-5, 10, 10, g.Color)
		textX := dotX + 22
		label := g.Scorer
		if label == "" {
			label = "goal"
		}
		f.ui.TextS(label, textX, midY, theme.Body, theme.Text)
		if g.Detail != "" {
			f.ui.TextS(g.Detail, textX+90, midY, theme.Small, theme.TextDim)
		}
	}
}

// drawResultShootout renders, per side, a row of scored/missed dots plus the tally.
func (a *App) drawResultShootout(f frame, r ResultModel, col *colLayout) {
	f.sectionHeader("PENALTY SHOOTOUT", col.x, col.header(1), col.w)
	for i := 0; i < 2; i++ {
		y := col.row()
		if !f.draw {
			continue
		}
		midY := y + theme.RowH/2
		st := r.Shootout[i]
		f.ui.TextS(fitMenu(f, r.Teams[i].Name, 150, theme.Body), col.x, midY, theme.Body, r.Teams[i].Color)
		dx := col.x + 168
		const dr, dgap = 7.0, 22.0
		for _, scored := range st.Scored {
			if scored {
				f.ui.FillRect(dx-dr, midY-dr, dr*2, dr*2, theme.Accent)
			} else {
				f.ui.StrokeRect(dx-dr, midY-dr, dr*2, dr*2, 2, theme.Bad)
			}
			dx += dgap
		}
		f.ui.TextRightS(strconv.Itoa(st.Goals), col.x+col.w, midY, theme.Section, r.Teams[i].Color)
	}
}

// drawResultStats renders the scaffolded stats table (no recorder yet): every cell shows
// "—" with a footnote, the deliberate placeholder.
func (a *App) drawResultStats(f frame, col *colLayout) {
	f.sectionHeader("STATS", col.x, col.header(1), col.w)
	rows := []string{"Shots", "Possession", "Passes"}
	homeX := col.x + col.w*0.62
	awayX := col.x + col.w
	for _, label := range rows {
		y := col.row()
		if !f.draw {
			continue
		}
		midY := y + theme.RowH/2
		f.ui.TextS(label, col.x, midY, theme.Body, theme.Text)
		f.ui.TextCenteredS("—", homeX, midY, theme.Body, theme.TextDim)
		f.ui.TextRightS("—", awayX, midY, theme.Body, theme.TextDim)
	}
	y := col.row()
	if f.draw {
		f.ui.TextS("Detailed stats coming soon.", col.x, y+theme.RowH/2, theme.Small, theme.TextDim)
	}
}

// fitMenu truncates s with an ellipsis so its measured width fits within maxW at sizeUI.
// It measures with the same scale-independent MeasureUI used for layout, so the two
// immediate-mode passes agree.
func fitMenu(f frame, s string, maxW, sizeUI float64) string {
	if f.ui.MeasureUI(s, sizeUI) <= maxW {
		return s
	}
	for len(s) > 1 {
		s = s[:len(s)-1]
		if f.ui.MeasureUI(s+"…", sizeUI) <= maxW {
			return s + "…"
		}
	}
	return s
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

// dimLabel shows an optional dimension override (0 reads as "auto" = inherit the preset).
func dimLabel(v float64) string {
	if v <= 0 {
		return "auto"
	}
	return strconv.Itoa(int(v))
}

// stepDim steps an optional dimension override. Stepping up from "auto" (0) starts at
// base; stepping down past min snaps back to "auto" (inherit the preset).
func stepDim(v float64, d int, step, base, hi float64) float64 {
	if v <= 0 {
		if d > 0 {
			return base
		}
		return 0
	}
	v += float64(d) * step
	if v < base {
		return 0
	}
	if v > hi {
		return hi
	}
	return v
}

// penBestLabel shows the shootout length (0 reads as the default of 5).
func penBestLabel(n int) string {
	if n <= 0 {
		return "5 (default)"
	}
	return strconv.Itoa(n)
}

// disabledRow draws a greyed-out informational row in place of an interactive widget
// (used for box sub-rows that are inactive because the box is off or keeper-only).
func (f frame) disabledRow(label string, x, y, w float64) {
	if f.draw {
		f.ui.TextS(label, x, y+theme.RowH/2, theme.Body, theme.TextDim)
	}
}
