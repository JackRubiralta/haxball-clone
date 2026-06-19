package menu

import (
	"context"
	"log/slog"
	"runtime/debug"
	"strconv"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"phootball/internal/audio"
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
