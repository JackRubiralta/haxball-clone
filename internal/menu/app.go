package menu

import (
	"context"
	"image/color"
	"log/slog"
	"math"
	"runtime/debug"
	"strconv"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"phootball/internal/audio"
	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/input"
	"phootball/internal/netcode"
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

	// Multiplayer screens (LAN). The in-match StatePlaying/Paused/Result are reused on the
	// networked path, distinguished by a non-nil a.net.
	StateMPHome         // Host / Join chooser
	StateMPJoin         // address entry
	StateMPConnecting   // background connect with spinner / error variant
	StateMPLobby        // roster, team/slot picking, ready, host controls
	StateMPReconnecting // a dropped client redialing with its resume token
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
	setupScroll    [5]scrollState
	settingsScroll scrollState

	camera *render.Camera
	audio  *audio.Manager

	// The transforms last drawn this frame, used to invert the cursor in the next update
	// pass without any render-package global: worldViewport for in-match aim (camera
	// pan/zoom), uiViewport for menu hit-testing.
	worldViewport render.Viewport
	uiViewport    render.Viewport

	practice bool
	human    bool
	quit     bool

	// Stats: statsHUD toggles the live in-match stats panel (Tab); recordWritten guards the
	// one-shot JSON persist on the transition to the result screen.
	statsHUD      bool
	recordWritten bool

	// Duo testing mode: one human switches between two players with 1 and 2.
	duo      bool
	duoHuman *input.Human
	activeID int

	// Multiplayer (LAN). net is non-nil while a networked match/lobby is live and selects the
	// snapshot-follow path. mpRole marks Match Setup as host-config mode. The connect runs in the
	// background and reports on dialCh; netErr/pendingAddr drive the connecting screen. mpName /
	// mpAddr are the persisted player name and last join address; focusField is the focused text box.
	net          *netSession
	mpRole       int
	dialCh       chan dialOutcome
	dialSrv      *netcode.Server
	dialCancel   context.CancelFunc
	netErr       string
	pendingAddr  string
	mpName       string
	mpAddr       string
	recentAddrs  []string
	focusField   *string
	caretBlink   int
	editingLobby bool // Match Setup was entered to edit an existing lobby (Apply -> CConfig)
}

// NewApp creates an app that opens on the main menu.
func NewApp(ctx context.Context, uc UserConfig) *App {
	return &App{
		ctx: ctx, state: StateMenu,
		settings:    uc.Settings, // match setup + physics Tuning, loaded from disk (or defaults)
		prefs:       uc.Prefs,    // camera/audio
		mpName:      uc.Net.Name,
		mpAddr:      uc.Net.LastAddr,
		recentAddrs: uc.Net.RecentAddrs,
		camera:      render.NewCamera(),
	}
}

// Prefs returns the current app prefs (camera/audio). The entry point reads these as the base
// for explicit CLI overrides, so a saved pref survives unless the user passes the flag.
func (a *App) Prefs() AppPrefs { return a.prefs }

// NewPlayingApp creates an app that starts straight into a prepared match (fast-path
// flags such as -solo).
func NewPlayingApp(ctx context.Context, m *sim.Match, controllers map[int]control.Controller, human bool) *App {
	a := &App{ctx: ctx, state: StatePlaying, match: m, controllers: controllers, human: human,
		settings: DefaultSettings(), prefs: DefaultAppPrefs(), camera: render.NewCamera()}
	m.EnableRecording() // so the Tab stats panel works in the fast-path (-solo/-ai-both) modes too
	a.camera.FocusID = a.humanFocusID()
	return a
}

// NewDuoApp starts the two-player switching tester.
func NewDuoApp(ctx context.Context, m *sim.Match) *App {
	m.EnableRecording() // so the Tab stats panel works in -duo too
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

// afterStep plays this tick's sounds and checks for a finished match. On the transition to
// the result screen it persists the match's stats/play-by-play JSON exactly once.
func (a *App) afterStep() {
	if a.audio != nil {
		a.audio.Dispatch(a.match.DrainEvents())
	}
	if a.match.Finished() {
		if !a.recordWritten {
			writeMatchRecord(a.match)
			a.recordWritten = true
		}
		a.state = StateResult
	}
}

// recoverNet is the last-line safety net for the networked paths: if a frame panics (e.g. an
// unforeseen nil-deref on a disconnect), it logs the panic, tears any session down, and drops to
// the main menu instead of hard-crashing the app. Used as a deferred call in Update/Draw.
func (a *App) recoverNet(where string) {
	if r := recover(); r != nil {
		slog.Error("recovered from panic; returning to menu", "where", where, "panic", r, "stack", string(debug.Stack()))
		a.leaveNetwork()
		a.match, a.controllers = nil, nil
		a.statsHUD = false
		a.state = StateMenu
	}
}

// Update advances the active screen.
func (a *App) Update() error {
	defer a.recoverNet("Update")
	select {
	case <-a.ctx.Done():
		return ebiten.Termination
	default:
	}
	a.caretBlink++
	switch a.state {
	case StatePlaying:
		if a.net != nil {
			a.updateMPPlaying()
		} else {
			a.updatePlaying()
		}
	case StateMenu:
		a.screenMenu(a.updateFrame())
	case StateMatchSetup:
		a.screenMatchSetup(a.updateFrame())
	case StateSettings:
		a.screenSettings(a.updateFrame())
	case StatePaused:
		a.screenPaused(a.updateFrame())
	case StateResult:
		if a.net != nil {
			a.updateMPResult()
			// updateMPResult may have torn the session down (disconnect/host-leave) and changed
			// state; only draw the networked result if we're still in it.
			if a.state == StateResult && a.net != nil {
				a.screenMPResult(a.updateFrame())
			}
		} else {
			a.screenResult(a.updateFrame())
		}
	case StateMPHome:
		a.screenMPHome(a.updateFrame())
	case StateMPJoin:
		a.screenMPJoin(a.updateFrame())
	case StateMPConnecting:
		a.pumpDial()
		a.screenMPConnecting(a.updateFrame())
	case StateMPLobby:
		a.updateMPLobby()
		if a.state == StateMPLobby && a.net != nil { // updateMPLobby may have disconnected us
			a.screenMPLobby(a.updateFrame())
		}
	case StateMPReconnecting:
		a.pumpReconnect()
		a.screenMPReconnecting(a.updateFrame())
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
	// Tab toggles the live stats panel (built from the match recorder).
	if inpututil.IsKeyJustPressed(ebiten.KeyTab) {
		a.statsHUD = !a.statsHUD
	}
	if a.duo {
		if inpututil.IsKeyJustPressed(ebiten.KeyDigit1) {
			a.activeID = 0
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyDigit2) {
			a.activeID = 1
		}
		a.duoHuman.SetViewport(a.worldViewport)
		a.match.Step(map[int]sim.Intent{a.activeID: a.duoHuman.Intent(a.match.View())}, dt)
		a.afterStep()
		return
	}
	// Tell each human controller which frame transform to invert the cursor with (the
	// camera viewport from the last draw), so aim is correct at any pan/zoom.
	for _, c := range a.controllers {
		if h, ok := c.(*input.Human); ok {
			h.SetViewport(a.worldViewport)
		}
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
	defer a.recoverNet("Draw")
	switch a.state {
	case StatePlaying:
		if a.net != nil {
			a.drawMPWorld(screen)
		} else {
			a.worldViewport = render.Frame(screen, a.match, a.camera, dt)
			if a.statsHUD {
				render.StatsPanel(screen, render.StatsModelFromMatch(a.match))
			}
		}
	case StatePaused:
		if a.net != nil {
			a.drawMPWorld(screen)
		} else {
			a.worldViewport = render.Frame(screen, a.match, a.camera, dt)
		}
		a.screenPaused(a.drawFrame(screen))
	case StateResult:
		if a.net != nil {
			a.drawMPWorld(screen)
			a.screenMPResult(a.drawFrame(screen))
		} else {
			a.worldViewport = render.Frame(screen, a.match, a.camera, dt)
			a.screenResult(a.drawFrame(screen))
		}
	case StateMenu:
		a.screenMenu(a.drawFrame(screen))
	case StateMatchSetup:
		a.screenMatchSetup(a.drawFrame(screen))
	case StateSettings:
		a.screenSettings(a.drawFrame(screen))
	case StateMPHome:
		a.screenMPHome(a.drawFrame(screen))
	case StateMPJoin:
		a.screenMPJoin(a.drawFrame(screen))
	case StateMPConnecting:
		a.screenMPConnecting(a.drawFrame(screen))
	case StateMPLobby:
		a.screenMPLobby(a.drawFrame(screen))
	case StateMPReconnecting:
		a.drawMPWorld(screen) // freeze the last frame behind the reconnect overlay
		a.screenMPReconnecting(a.drawFrame(screen))
	}
}

// drawMPWorld renders the networked match world from the latest snapshot, rebuilding the cached
// field only when the geometry changes. Records the viewport so the next update maps the cursor to
// world aim.
func (a *App) drawMPWorld(screen *ebiten.Image) {
	if a.net == nil {
		screen.Fill(theme.BG)
		return
	}
	snap, ok := a.net.client.Snapshot()
	if !ok {
		screen.Fill(theme.BG)
		return
	}
	if a.net.field == nil || snap.Geometry != a.net.geo {
		a.net.field = sim.NewFieldFromGeometry(snap.Geometry)
		a.net.geo = snap.Geometry
	}
	a.worldViewport = render.FrameFromSnapshot(screen, a.adaptSnapshot(snap), a.net.field, a.statsHUD)

	// Honest no-prediction framing: a corner latency badge, plus a one-time toast since inputs are
	// round-trip-delayed (the client is a pure follower, no prediction).
	ui := render.BeginUI(screen)
	if rtt := a.net.client.RTTms(); rtt > 0 {
		ui.TextRightS(latencyLabel(rtt), render.UIWidth-14, 16, theme.Small, latencyColor(rtt))
	}
	if a.net.toast > 0 {
		ui.TextCenteredS("Online play — your inputs take a moment to reach the host",
			render.UIWidth/2, render.UIHeight-28, theme.Small, theme.TextDim)
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

// DebugSeedNet seeds the fields the multiplayer screens read; for screenshot/debug tooling only.
func (a *App) DebugSeedNet(mode string) {
	a.mpName = "Jack"
	a.mpAddr = "192.168.1.20:47600"
	a.recentAddrs = []string{"192.168.1.20:47600", "10.0.0.5:47600"}
	a.pendingAddr = "192.168.1.20:47600"
	if mode == "error" {
		a.netErr = "Couldn't reach the host — check the address and that the host is running.\n(dial tcp 192.168.1.20:47600: connect: connection refused)"
	}
}

// quitToMenu tears down the current match and returns to the main menu, clearing BOTH the
// match and its controllers so no stale controller map survives into the next match.
func (a *App) quitToMenu() {
	a.leaveNetwork() // idempotent: closes the client + cancels the in-proc server if networked
	a.match = nil
	a.controllers = nil
	a.statsHUD = false
	a.state = StateMenu
}

func (a *App) startMatch(practice, human bool) {
	a.practice, a.human = practice, human
	a.match, a.controllers = a.settings.BuildMatch(practice, human)
	a.match.EnableRecording() // capture stats + play-by-play for the live HUD and the result JSON
	a.recordWritten = false
	a.statsHUD = false
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
		a.mpRole = roleNone
		a.state = StateMatchSetup
	}
	y += bh + 18
	if f.button("Multiplayer", bx, y, bw, bh) {
		a.state = StateMPHome
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
var setupTabs = []string{"Teams & Control", "Pitch & Goals", "Boxes", "Match Rules", "Tuning"}

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
	case 4:
		a.setupTuning(cf, &col)
	}
	f.endScroll(sc, paneX, paneY, paneW, paneH, col.cursorY())

	// Action bar: Back (left), validation banner (centre), and the primary action (right, gated).
	// The SAME validated config screen serves three modes: a solo Start, the host's Create Lobby,
	// and the host editing a live lobby (Apply -> CConfig).
	const abw = 180.0
	editing := a.editingLobby
	hosting := a.mpRole == roleHost && !editing
	if f.button("Back", railX, barY, abw, barH) {
		switch {
		case editing:
			a.editingLobby = false
			a.state = StateMPLobby // discard edits
		case hosting:
			a.state = StateMPHome
		default:
			a.state = StateMenu
		}
	}
	err := a.settings.Validate()
	if f.draw && err != nil {
		f.ui.TextCenteredS(err.Error(), render.UIWidth/2, barY+barH/2, theme.Small, theme.Bad)
	}
	startLabel := "Start"
	switch {
	case editing:
		startLabel = "Apply"
	case hosting:
		startLabel = "Create Lobby"
	}
	startX := px + pw - pad - abw
	if err == nil {
		if f.button(startLabel, startX, barY, abw, barH) {
			a.saveUserConfig() // persist the committed match setup + tuning + prefs (Start/Create/Apply)
			switch {
			case editing:
				a.editingLobby = false
				if a.net != nil {
					a.net.client.SendConfig(a.settings.MatchSetup)
				}
				a.state = StateMPLobby
			case hosting:
				a.beginHost(defaultMPListenAddr)
			default:
				a.startSetup()
			}
		}
	} else if f.draw {
		// Disabled action: drawn dim, not clickable.
		f.ui.FillRect(startX, barY, abw, barH, theme.BtnBG)
		f.ui.StrokeRect(startX, barY, abw, barH, 2, theme.Edge)
		f.ui.TextCenteredS(startLabel, startX+abw/2, barY+barH/2, theme.Body, theme.TextDim)
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
		if d, i := f.rowStepper("Size", strconv.Itoa(tc.Size), c.x, c.row(), c.w, float64(tc.Size), 1, 11); d || i {
			tc.Size = clampInt(tc.Size+dir(i), 1, 11)
			s.ClampDependents()
		}
		if tc.Human {
			if d, i := f.rowStepper("Human slot", strconv.Itoa(tc.HumanSlot), c.x, c.row(), c.w, float64(tc.HumanSlot), 1, float64(tc.Size)); d || i {
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
	labels := [3]string{"Small", "Medium", "Large"} // index-aligned with fieldPresets (small, standard, large)
	by := col.row()
	bw := (col.w - 2*theme.PanelPad) / 3
	bh := theme.RowH - 10
	selected := s.SelectedPreset() // "" unless the current dims exactly match a preset
	for i, name := range fieldPresets {
		if f.selectButton(labels[i], name == selected, col.x+float64(i)*(bw+theme.PanelPad), by, bw, bh) {
			s.ApplyPreset(name)
		}
	}

	// Dimensions show the effective value (resolving an inherited preset to a concrete number --
	// never "auto"); editing one makes it an explicit override clamped to [min, max].
	pw, ph := s.effectivePitch()
	if d, i := f.rowStepper("Pitch length", strconv.Itoa(int(pw)), col.x, col.row(), col.w, pw, 400, 2400); d || i {
		s.PlayWidth = clampF(pw+float64(dir(i))*40, 400, 2400)
		s.ClampDependents()
	}
	if d, i := f.rowStepper("Pitch width", strconv.Itoa(int(ph)), col.x, col.row(), col.w, ph, 240, 1600); d || i {
		s.PlayHeight = clampF(ph+float64(dir(i))*40, 240, 1600)
		s.ClampDependents()
	}
	if d, i := f.rowStepper("Goal width", strconv.Itoa(int(s.GoalWidth)), col.x, col.row(), col.w, s.GoalWidth, 40, 240); d || i {
		s.GoalWidth = clampF(s.GoalWidth+float64(dir(i))*10, 40, 240)
		s.ClampDependents()
	}
	gd := s.effectiveGoalDepth()
	if d, i := f.rowStepper("Goal depth", strconv.Itoa(int(gd)), col.x, col.row(), col.w, gd, 10, 80); d || i {
		s.GoalDepth = clampF(gd+float64(dir(i))*5, 10, 80)
		s.ClampDependents()
	}
	// Centre circle, shown and bounded by its DIAMETER (max = half the pitch length). The ball
	// kicks off in here; players start outside it (the conceding side gets a taker inside).
	if d, i := f.rowStepper("Centre circle Ø", strconv.Itoa(int(2*s.effectiveCircle())), col.x, col.row(), col.w, s.effectiveCircle(), s.circleMin(), s.circleMax()); d || i {
		s.stepCircle(dir(i))
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
		if d, i := f.rowStepper("Width", strconv.Itoa(int(s.PenaltyWidth)), col.x, col.row(), col.w, s.PenaltyWidth, 40, 800); d || i {
			s.PenaltyWidth = clampF(s.PenaltyWidth+float64(dir(i))*20, 40, 800)
			s.ClampDependents()
		}
		if d, i := f.rowStepper("Depth", strconv.Itoa(int(s.PenaltyDepth)), col.x, col.row(), col.w, s.PenaltyDepth, 20, 600); d || i {
			s.PenaltyDepth = clampF(s.PenaltyDepth+float64(dir(i))*10, 20, 600)
			s.ClampDependents()
		}
		if d, i := f.rowStepper("Max defenders", capLabel(s.PenaltyBoxMax), col.x, col.row(), col.w, float64(s.PenaltyBoxMax), 0, 11); d || i {
			s.PenaltyBoxMax = clampInt(s.PenaltyBoxMax+dir(i), 0, 11)
		}
		if d, i := f.rowStepper("Max attackers", capLabel(s.PenaltyBoxMaxOpp), col.x, col.row(), col.w, float64(s.PenaltyBoxMaxOpp), 0, 11); d || i {
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
		if d, i := f.rowStepper("Width", strconv.Itoa(int(s.GoalAreaWidth)), col.x, col.row(), col.w, s.GoalAreaWidth, 40, 700); d || i {
			s.GoalAreaWidth = clampF(s.GoalAreaWidth+float64(dir(i))*20, 40, 700)
			s.ClampDependents()
		}
		if d, i := f.rowStepper("Depth", strconv.Itoa(int(s.GoalAreaDepth)), col.x, col.row(), col.w, s.GoalAreaDepth, 20, 500); d || i {
			s.GoalAreaDepth = clampF(s.GoalAreaDepth+float64(dir(i))*10, 20, 500)
			s.ClampDependents()
		}
		if f.rowToggle("Keeper-only", s.GoalAreaKeeperOnly, col.x, col.row(), col.w) {
			s.GoalAreaKeeperOnly = !s.GoalAreaKeeperOnly
		}
		if s.GoalAreaKeeperOnly {
			f.disabledRow("Max defenders / attackers: keeper-only", col.x, col.row(), col.w)
		} else {
			if d, i := f.rowStepper("Max defenders", capLabel(s.GoalAreaMax), col.x, col.row(), col.w, float64(s.GoalAreaMax), 0, 11); d || i {
				s.GoalAreaMax = clampInt(s.GoalAreaMax+dir(i), 0, 11)
			}
			if d, i := f.rowStepper("Max attackers", capLabel(s.GoalAreaMaxOpp), col.x, col.row(), col.w, float64(s.GoalAreaMaxOpp), 0, 11); d || i {
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
		if d, i := f.rowStepper("Goals to win", strconv.Itoa(s.WinScore), col.x, col.row(), col.w, float64(s.WinScore), 1, 20); d || i {
			s.WinScore = clampInt(s.WinScore+dir(i), 1, 20)
		}
	}
	if f.rowToggle("Time limit", s.WinByTime, col.x, col.row(), col.w) {
		s.WinByTime = !s.WinByTime
	}
	if s.WinByTime {
		if d, i := f.rowStepper("Minutes", strconv.Itoa(int(s.Minutes)), col.x, col.row(), col.w, s.Minutes, 1, 30); d || i {
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
				if d, i := f.rowStepper("Minutes", strconv.Itoa(int(s.ExtraMinutes)), col.x, col.row(), col.w, s.ExtraMinutes, 1, 30); d || i {
					s.ExtraMinutes = clampF(s.ExtraMinutes+float64(dir(i)), 1, 30)
				}
			}
		} else {
			if d, i := f.rowStepper("Extra minutes", strconv.Itoa(int(s.ExtraMinutes)), col.x, col.row(), col.w, s.ExtraMinutes, 1, 30); d || i {
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
		if d, i := f.rowStepper("Penalty kicks "+hint, penBestLabel(s.PenaltyBestOf), col.x, col.row(), col.w, float64(s.PenaltyBestOf), 1, 11); d || i {
			s.PenaltyBestOf = clampInt(s.PenaltyBestOf+dir(i), 1, 11)
		}
	}
	// Derived one-line summary of how a level match resolves (mirrors MatchSetup.Ruleset's
	// OnDraw chain). With neither extra time nor penalties enabled it simply reads "A draw
	// stands." -- so the old explicit toggle for that was redundant and is gone.
	f.disabledRow(drawSummary(s), col.x, col.row(), col.w)
}

// tuneRow draws one tuning stepper bound to *v, clamped to [lo,hi] and stepping by `step` (in
// STORED units). disp formats the stored value for display (integer, decimal, percent, degrees).
func (a *App) tuneRow(f frame, col *colLayout, label string, v *float64, lo, hi, step float64, disp func(float64) string) {
	if d, i := f.rowStepper(label, disp(*v), col.x, col.row(), col.w, *v, lo, hi); d || i {
		*v = clampF(*v+float64(dir(i))*step, lo, hi)
	}
}

func tfInt(v float64) string  { return strconv.Itoa(int(math.Round(v))) }
func tfDec1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
func tfDec2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
func tfPct(v float64) string  { return strconv.Itoa(int(math.Round(v*100))) + "%" }
func tfDeg(v float64) string  { return strconv.Itoa(int(math.Round(v*180/math.Pi))) + "°" }

// setupTuning draws the per-match TUNING editor: every numeric physics/gameplay knob in the
// README "Physics & player variables" section, grouped into sections. The angle-curve SHAPES
// are hardcoded (never shown here) -- only the endpoints/scalars are editable. Edits write into
// s.Tuning (a copy of DefaultTuning), which MatchSetup.Build folds into the match config and
// the netcode wire carries to a LAN server. "Reset all" restores the defaults.
func (a *App) setupTuning(f frame, col *colLayout) {
	s := &a.settings
	t := &s.Tuning
	p := &t.Player
	q := &p.TouchQuality
	pp := &t.Possession
	deg := math.Pi / 180

	if f.selectButton("Reset all to defaults", false, col.x, col.row(), col.w, theme.RowH-10) {
		s.Tuning = config.DefaultTuning()
	}

	f.sectionHeader("BODY & MOTION", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Radius", &p.Radius, 6, 40, 1, tfInt)
	a.tuneRow(f, col, "Mass", &p.Mass, 1, 100, 1, tfInt)
	a.tuneRow(f, col, "Friction", &p.Friction, -5, 0, 0.1, tfDec1)
	a.tuneRow(f, col, "Max speed", &p.MaxSpeed, 40, 400, 5, tfInt)
	a.tuneRow(f, col, "Acceleration", &p.Acceleration, 50, 800, 10, tfInt)
	a.tuneRow(f, col, "Turn rate", &p.TurnRate, 2, 40, 1, tfInt)

	f.sectionHeader("BALL CONTROL (reach)", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Touch range", &p.TouchRange, 0, 20, 0.5, tfDec1)
	a.tuneRow(f, col, "Pull range", &p.PullRange, 0, 40, 0.5, tfDec1)
	a.tuneRow(f, col, "Possession range", &p.PossessionRange, 0, 40, 0.5, tfDec1)

	f.sectionHeader("ANGLE CURVES (front / back)", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Restitution (front)", &p.Restitution.Front, 0, 2, 0.01, tfDec2)
	a.tuneRow(f, col, "Restitution (back)", &p.Restitution.Back, 0, 2, 0.01, tfDec2)
	a.tuneRow(f, col, "Capture speed (front)", &p.CaptureSpeed.Front, 0, 600, 5, tfInt)
	a.tuneRow(f, col, "Capture speed (back)", &p.CaptureSpeed.Back, 0, 600, 5, tfInt)
	a.tuneRow(f, col, "Centre pull (front)", &p.CenterPull.Front, 0, 2000, 10, tfInt)
	a.tuneRow(f, col, "Centre pull (back)", &p.CenterPull.Back, 0, 2000, 10, tfInt)
	a.tuneRow(f, col, "Stickiness (front)", &p.Stickiness.Front, 0, 1000, 10, tfInt)
	a.tuneRow(f, col, "Stickiness (back)", &p.Stickiness.Back, 0, 1000, 10, tfInt)
	a.tuneRow(f, col, "Control (front)", &p.Control.Front, 0, 3000, 10, tfInt)
	a.tuneRow(f, col, "Control (back)", &p.Control.Back, 0, 3000, 10, tfInt)
	a.tuneRow(f, col, "Shot power (front)", &p.Shoot.Front, 50, 1500, 25, tfInt)
	a.tuneRow(f, col, "Shot power (back)", &p.Shoot.Back, 0, 1000, 5, tfInt)

	f.sectionHeader("CONES (degrees)", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Capture cone", &p.CaptureConeRadians, 0, math.Pi, deg, tfDeg)
	a.tuneRow(f, col, "Capture cone soft", &p.CaptureConeSoft, 0, math.Pi, deg, tfDeg)
	a.tuneRow(f, col, "Control cone", &p.ControlConeRadians, 0, math.Pi, deg, tfDeg)
	a.tuneRow(f, col, "Control cone +possession", &p.ControlConePossessionBonus, 0, math.Pi/2, deg, tfDeg)
	a.tuneRow(f, col, "Capture cone +trap", &p.CaptureConeTrapBonus, 0, math.Pi/2, deg, tfDeg)
	a.tuneRow(f, col, "Control cone +trap", &p.ControlConeTrapBonus, 0, math.Pi/2, deg, tfDeg)
	a.tuneRow(f, col, "Centre-pull cone", &p.CenterPullConeRadians, 0, math.Pi, deg, tfDeg)
	a.tuneRow(f, col, "Centre-pull cone +poss", &p.CenterPullConePossessionBonus, 0, math.Pi/2, deg, tfDeg)
	a.tuneRow(f, col, "Centre-pull cone +trap", &p.CenterPullConeTrapBonus, 0, math.Pi/2, deg, tfDeg)

	f.sectionHeader("DAMPING & HOLD", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Control damping", &p.ControlDamping, 0, 50, 1, tfInt)
	a.tuneRow(f, col, "Orbit stick", &p.OrbitStick, 0, 50, 1, tfInt)
	a.tuneRow(f, col, "Seat strength", &p.SeatStrength, 0, 50, 1, tfInt)

	f.sectionHeader("PLAYER POSSESSION", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Build seconds", &p.PossessionBuildSeconds, 0.1, 10, 0.1, tfDec1)
	a.tuneRow(f, col, "Release seconds", &p.PossessionReleaseSeconds, 0.1, 10, 0.1, tfDec1)
	a.tuneRow(f, col, "Centre-pull grip floor", &p.CenterPullGripFloor, 0, 1, 0.01, tfPct)
	a.tuneRow(f, col, "Stickiness debuff", &p.StickinessPossessionDebuff, 0, 1, 0.01, tfPct)
	a.tuneRow(f, col, "Speed factor", &p.PossessionSpeedFactor, 0.3, 1, 0.01, tfPct)
	a.tuneRow(f, col, "Accel factor", &p.PossessionAccelFactor, 0.3, 1, 0.01, tfPct)
	a.tuneRow(f, col, "Control bonus", &p.PossessionControlBonus, 0, 1, 0.01, tfPct)
	a.tuneRow(f, col, "Steal rate", &p.PossessionStealRate, 0, 5, 0.1, tfDec1)

	f.sectionHeader("CHARGED SHOT & AIM", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Min shot (tap)", &p.MinShootFactor, 0, 1, 0.01, tfPct)
	a.tuneRow(f, col, "Shot speed factor", &p.ShootSpeedFactor, 0.1, 1, 0.01, tfPct)
	a.tuneRow(f, col, "Shot accel factor", &p.ShootAccelFactor, 0.1, 1, 0.01, tfPct)
	a.tuneRow(f, col, "Aim assist", &p.ShootAimAssist, 0, 1, 0.01, tfPct)

	f.sectionHeader("TRAP (good touch)", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Pull bonus", &p.TrapPullBonus, 0, 5, 0.1, tfDec1)
	a.tuneRow(f, col, "Range bonus", &p.TrapRangeBonus, 0, 30, 1, tfInt)
	a.tuneRow(f, col, "Control bonus", &p.TrapControlBonus, 0, 5, 0.05, tfDec2)
	a.tuneRow(f, col, "Stickiness bonus", &p.TrapStickinessBonus, 0, 3, 0.05, tfDec2)
	a.tuneRow(f, col, "Accel factor", &p.TrapAccelFactor, 0.1, 1, 0.01, tfPct)
	a.tuneRow(f, col, "Speed factor", &p.TrapSpeedFactor, 0.1, 1, 0.01, tfPct)
	a.tuneRow(f, col, "Capture bonus", &p.TrapCaptureBonus, 0, 300, 5, tfInt)
	a.tuneRow(f, col, "Radius bonus", &p.TrapRadiusBonus, 0, 30, 1, tfInt)
	a.tuneRow(f, col, "Restitution factor", &p.TrapRestitutionFactor, 0, 2, 0.05, tfDec2)

	f.sectionHeader("TEAM POSSESSION — TOUCH QUALITY", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Own team (max)", &q.OwnTeamMax, 0, 2, 0.05, tfDec2)
	a.tuneRow(f, col, "Other team", &q.OtherTeam, -2, 0, 0.05, tfDec2)
	a.tuneRow(f, col, "Capture worst", &q.CaptureWorst, 0, 2, 0.01, tfDec2)
	a.tuneRow(f, col, "Capture best", &q.CaptureBest, 0, 2, 0.01, tfDec2)
	a.tuneRow(f, col, "Restitution worst", &q.RestitutionWorst, 0, 4, 0.05, tfDec2)
	a.tuneRow(f, col, "Restitution best", &q.RestitutionBest, 0, 4, 0.05, tfDec2)
	a.tuneRow(f, col, "Cone bonus", &q.ConeBonusRadians, 0, math.Pi/2, deg, tfDeg)
	a.tuneRow(f, col, "Cone debuff", &q.ConeDebuffRadians, 0, math.Pi/2, deg, tfDeg)

	f.sectionHeader("TEAM POSSESSION — DURATIONS", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Build seconds", &pp.BuildSeconds, 0.1, 10, 0.1, tfDec1)
	a.tuneRow(f, col, "Hold seconds", &pp.HoldSeconds, 0, 10, 0.1, tfDec1)
	a.tuneRow(f, col, "Decay seconds", &pp.DecaySeconds, 0.1, 20, 0.1, tfDec1)
	a.tuneRow(f, col, "Build exponent", &pp.BuildExponent, 0.5, 6, 0.1, tfDec1)
	a.tuneRow(f, col, "Drain per second", &pp.DrainPerSecond, 0, 5, 0.1, tfDec1)
	a.tuneRow(f, col, "Boost drain/sec", &pp.BoostContactDrainPerSecond, 0, 5, 0.1, tfDec1)
	a.tuneRow(f, col, "Boost recover/sec", &pp.BoostContactRecoverPerSecond, 0, 5, 0.1, tfDec1)

	f.sectionHeader("WORLD & BALL", col.x, col.header(1), col.w)
	a.tuneRow(f, col, "Ball radius", &t.BallRadius, 2, 30, 0.5, tfDec1)
	a.tuneRow(f, col, "Ball friction", &t.BallFriction, -2, 0, 0.05, tfDec2)
	a.tuneRow(f, col, "Ball mass", &t.BallMass, 0.1, 20, 0.1, tfDec1)
	a.tuneRow(f, col, "Ball wall bounce", &t.BallWallRestitution, 0, 1, 0.05, tfPct)
	a.tuneRow(f, col, "Player wall bounce", &t.PlayerWallRestitution, 0, 1, 0.05, tfPct)
	a.tuneRow(f, col, "Obstacle bounce", &t.ObstacleRestitution, 0, 1, 0.05, tfPct)
	a.tuneRow(f, col, "Net bounce", &t.NetRestitution, 0, 1, 0.05, tfPct)
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
	if d, i := cf.rowStepper("Mode", p.CameraMode, col.x, col.row(), col.w, 0, 0, -1); d || i { // hi<lo: a cycling stepper (both arrows always active)
		p.CameraMode = cycle(cameraPresets, p.CameraMode, dir(i))
		a.applyPrefs()
	}
	if d, i := cf.rowStepper("Zoom", strconv.FormatFloat(p.Zoom, 'f', 2, 64)+"x", col.x, col.row(), col.w, p.Zoom, 1, 4); d || i {
		p.Zoom = clampF(p.Zoom+float64(dir(i))*0.25, 1, 4)
		a.applyPrefs()
	}
	col.gapRow(0.3)
	cf.sectionHeader("AUDIO", col.x, col.header(1), col.w)
	if d, i := cf.rowStepper("Volume", strconv.Itoa(int(p.Volume*100+0.5))+"%", col.x, col.row(), col.w, p.Volume, 0, 1); d || i {
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
		a.saveUserConfig() // persist the edited camera/audio prefs on leaving the settings screen
		a.state = a.prevState
	}
}

func (a *App) screenPaused(f frame) {
	net := a.net != nil
	paused := false // whether the host has paused the match (server-side)
	if net {
		if snap, ok := a.net.client.Snapshot(); ok {
			paused = snap.Paused
		}
	}
	if f.draw {
		f.ui.DimScreen(theme.Overlay) // cover the WHOLE screen, not just the letterboxed UI box
		title := "PAUSED"
		switch {
		case net && paused:
			title = "MATCH PAUSED"
		case net:
			title = "MENU — game continues" // a client menu is view-only; the server keeps ticking
		}
		f.ui.Title(title, render.UIWidth/2, 170, theme.H1, theme.Accent)
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
	if net {
		// Networked: the host owns the match -- pause for everyone, or stop the game back to the
		// lobby (everyone stays connected). "Return to Lobby" IS the end-the-game action; fully
		// disconnecting is done from the lobby's Disconnect button. A guest can only leave.
		if a.net.isHost {
			pLabel := "Pause for everyone"
			if paused {
				pLabel = "Unpause match"
			}
			if f.button(pLabel, bx, y, bw, bh) {
				a.net.client.SetPaused(!paused) // server-side; the menu stays open to toggle back
			}
			y += bh + 16
			if f.button("Return to Lobby", bx, y, bw, bh) {
				a.net.client.SetPaused(false)
				a.net.client.ReturnToLobby() // ends the game; rebuilds the match, keeps everyone connected
				a.state = StatePlaying       // updateMPPlaying sees InLobby() -> StateMPLobby
			}
		} else if f.button("Leave Match", bx, y, bw, bh) {
			a.quitToMenu()
		}
		return
	}
	if !a.duo && f.button("Restart", bx, y, bw, bh) {
		a.startMatch(a.practice, a.human)
	}
	y += bh + 16
	if f.button("Quit to Menu", bx, y, bw, bh) {
		a.quitToMenu()
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
		f.ui.DimScreen(resultOverlay) // cover the WHOLE screen, not just the letterboxed UI box
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
		a.quitToMenu()
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

// drawResultStats renders the final team stat sheet from the match recorder (home = Blue/left,
// away = Red/right).
func (a *App) drawResultStats(f frame, col *colLayout) {
	f.sectionHeader("STATS", col.x, col.header(1), col.w)
	homeX := col.x + col.w*0.62
	awayX := col.x + col.w
	if a.match == nil {
		return
	}
	sm := render.StatsModelFromMatch(a.match)
	rows := []struct{ label, home, away string }{
		{"Possession", resPct(sm.Left.PossessionPct), resPct(sm.Right.PossessionPct)},
		{"Shots (on tgt)", resShots(sm.Left.Shots, sm.Left.OnTarget), resShots(sm.Right.Shots, sm.Right.OnTarget)},
		{"Passes", resPasses(sm.Left.PassesDone, sm.Left.Passes), resPasses(sm.Right.PassesDone, sm.Right.Passes)},
		{"Interceptions", strconv.Itoa(sm.Left.Interceptions), strconv.Itoa(sm.Right.Interceptions)},
		{"Saves", strconv.Itoa(sm.Left.Saves), strconv.Itoa(sm.Right.Saves)},
	}
	for _, r := range rows {
		y := col.row()
		if !f.draw {
			continue
		}
		midY := y + theme.RowH/2
		f.ui.TextS(r.label, col.x, midY, theme.Body, theme.Text)
		f.ui.TextCenteredS(r.home, homeX, midY, theme.Body, theme.Text)
		f.ui.TextRightS(r.away, awayX, midY, theme.Body, theme.Text)
	}
}

func resPct(v float64) string { return strconv.Itoa(int(v+0.5)) + "%" }
func resShots(shots, onTarget int) string {
	return strconv.Itoa(shots) + " (" + strconv.Itoa(onTarget) + ")"
}
func resPasses(done, total int) string {
	s := strconv.Itoa(done) + "/" + strconv.Itoa(total)
	if total > 0 {
		s += " (" + strconv.Itoa(int(100*float64(done)/float64(total)+0.5)) + "%)"
	}
	return s
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
