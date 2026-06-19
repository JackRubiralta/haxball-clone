package menu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/input"
	"phootball/internal/netcode"
	"phootball/internal/render"
	"phootball/internal/sim"
)

var errReconnectTimeout = errors.New("reconnect timed out")

const (
	defaultMPPort       = "47600"
	defaultMPListenAddr = ":" + defaultMPPort          // host listens on all interfaces (LAN-reachable)
	defaultMPDialAddr   = "127.0.0.1:" + defaultMPPort // address pre-filled on the Join screen
)

// loopbackAddr is the 127.0.0.1 address the host uses to dial its own listener, taking the port
// from the listen address.
func loopbackAddr(listen string) string {
	if _, port, err := net.SplitHostPort(listen); err == nil && port != "" {
		return "127.0.0.1:" + port
	}
	return defaultMPDialAddr
}

// Multiplayer roles for the Match Setup screen (host authors the config there).
const (
	roleNone = iota
	roleHost
	roleGuest
)

// netSession holds the App's networking state for a multiplayer match. The host variant also owns
// the in-process server goroutine and its cancel. There is exactly one local human (the snapshot
// client), host or guest alike; only host-only controls differ.
type netSession struct {
	client    *netcode.Client
	isHost    bool
	srv       *netcode.Server
	srvCancel context.CancelFunc
	human     *input.Human

	field         *sim.Field      // cached; rebuilt only when the snapshot geometry changes
	geo           config.Geometry // the geometry the cached field was built from
	lastSoundTick uint64          // audio dedupe across snapshots; reset on each (re)entry to play

	addr       string
	share      string // host only: the LAN address others type into Join (e.g. 192.168.1.x:47600)
	name       string
	token      string    // our session token, for reconnect
	pingFrames int       // frames since the last latency ping
	toast      int       // frames remaining on the one-time "inputs take a moment" toast
	deadline   time.Time // reconnect cutoff (zero unless reconnecting)
}

// lanAddress returns the address others on the LAN would type into Join: the first non-loopback
// private IPv4 of this machine plus port, or 127.0.0.1:port if none is found.
func lanAddress(port string) string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil {
				continue // skip IPv6
			}
			if ip[0] == 10 || (ip[0] == 192 && ip[1] == 168) || (ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31) {
				return net.JoinHostPort(ip.String(), port)
			}
		}
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// dialOutcome is the result of a background connect attempt, delivered on a.dialCh. The host's
// server + cancel are created synchronously in beginHost (held on the App) so a cancel mid-connect
// never orphans the server; only the loopback dial runs in the background.
type dialOutcome struct {
	client *netcode.Client
	isHost bool
	resume bool // a reconnect (keep playing) rather than a fresh connect
	err    error
}

const latencyToastFrames = 240 // ~4s one-time toast on the first networked match

// netName returns the local display name, defaulting when unset.
func (a *App) netName() string {
	if a.mpName == "" {
		return "Player"
	}
	return a.mpName
}

// randToken returns an unguessable session/host token.
func randToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "host-token" // fallback; loopback-only, low stakes
	}
	return hex.EncodeToString(b[:])
}

// buildHostMatch builds the authoritative match for the host from a Settings: the match (recording
// on), an AI bot for every player at its team's difficulty, and the claimable seats (one outfielder
// per team for the immediate auto-seat / host seat; the lobby lets guests pick any slot). Mirrors
// cmd/server but honours per-team sizes and difficulties.
func buildHostMatch(s Settings) (*sim.Match, map[int]netcode.Bot, []int) {
	cfg := s.Config()
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	home, away := s.Teams[teamHome].Size, s.Teams[teamAway].Size
	if home < 1 {
		home = 1
	}
	if away < 1 {
		away = 1
	}
	m := sim.BuildMatchFromConfigSized(field, home, away, cfg)
	m.EnableRecording()

	bots := make(map[int]netcode.Bot, len(m.Players))
	for ti, t := range []*sim.Team{m.Teams[teamHome], m.Teams[teamAway]} {
		skill, _ := control.SkillFromString(s.Teams[ti].Difficulty)
		for _, p := range t.Players {
			bots[p.PlayerID] = control.NewAISkill(p.PlayerID, skill)
		}
	}
	humanIDs := m.ClaimableHumanIDs()
	return m, bots, humanIDs
}

// beginHost starts the in-process server (lobby mode) and dials its own listener as the host
// (loopback client). The server + its cancel are created here (held on the App) so cancelling
// mid-connect never orphans the server; only the loopback dial runs in the background.
func (a *App) beginHost(addr string) {
	a.resetPendingConnect() // tear down any prior attempt (e.g. an impatient Retry) so nothing leaks
	a.netErr = ""
	a.pendingAddr = addr
	a.state = StateMPConnecting

	hostSettings := a.settings // snapshot the host's config (incl. per-team difficulties)
	name := a.netName()
	m, bots, humanIDs := buildHostMatch(hostSettings)
	srv := netcode.NewServer(addr, m, bots, humanIDs)
	srv.EnableLobby(hostSettings.MatchSetup, func(setup config.MatchSetup) (*sim.Match, map[int]netcode.Bot, []int) {
		s := hostSettings
		s.MatchSetup = setup
		return buildHostMatch(s)
	})
	tok := randToken()
	srv.SetPendingHostToken(tok)

	// Bind the listener SYNCHRONOUSLY: a port conflict surfaces immediately as a clear error (not a
	// swallowed `go Run` error + a 2s spinner), and the loopback dial below can't race an async
	// listen. Retry briefly to ride out a previous host's socket still releasing.
	if err := bindWithRetry(srv); err != nil {
		a.netErr = friendlyBindErr(err)
		return // stay on the connecting screen's error variant
	}
	ctx, cancel := context.WithCancel(a.ctx)
	go srv.Run(ctx)
	a.dialSrv, a.dialCancel = srv, cancel

	dialAddr := loopbackAddr(srv.Addr()) // the resolved host:port
	a.dialCh = make(chan dialOutcome, 1)
	ch := a.dialCh
	go func() {
		client, err := dialRetry(func() (*netcode.Client, error) { return netcode.DialHost(dialAddr, name, tok) })
		ch <- dialOutcome{client: client, isHost: true, err: err}
	}()
}

// bindWithRetry binds the server's listener, retrying briefly so an immediate re-host can ride out
// the previous host's socket still being released.
func bindWithRetry(srv *netcode.Server) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = srv.Bind(); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}

func friendlyBindErr(err error) string {
	msg := "Couldn't start hosting on " + defaultMPListenAddr + " — the port may be in use (another host still shutting down?). Try again."
	if err != nil {
		msg += "\n(" + err.Error() + ")"
	}
	return msg
}

// resetPendingConnect tears down any in-flight connect AND any live session, so a fresh begin*/Retry
// never leaks the previous server or drops its outcome on an orphaned channel. It deliberately does
// NOT touch mpRole/netErr (the caller manages those).
func (a *App) resetPendingConnect() {
	if a.dialCancel != nil {
		a.dialCancel()
	}
	a.dialSrv, a.dialCancel, a.dialCh = nil, nil, nil
	if a.net != nil {
		if a.net.client != nil {
			a.net.client.Close()
		}
		if a.net.srvCancel != nil {
			a.net.srvCancel()
		}
		a.net = nil
	}
}

// beginGuest dials a remote host in the background.
func (a *App) beginGuest(addr string) {
	a.resetPendingConnect() // tear down any prior attempt so nothing leaks on a Retry
	a.netErr = ""
	a.pendingAddr = addr
	a.dialCh = make(chan dialOutcome, 1)
	a.state = StateMPConnecting

	a.saveUserConfig() // persist the name + recents on an explicit connect
	name := a.netName()
	ch := a.dialCh
	go func() {
		client, err := netcode.DialJoin(addr, name)
		ch <- dialOutcome{client: client, err: err}
	}()
}

// dialRetry retries a dial briefly while a just-launched loopback listener binds.
func dialRetry(dial func() (*netcode.Client, error)) (*netcode.Client, error) {
	var lastErr error
	for i := 0; i < 100; i++ {
		c, err := dial()
		if err == nil {
			return c, nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	return nil, lastErr
}

// pumpDial polls the background connect. On success it installs the session and moves to the lobby
// (a mid-match join will fall straight through to play once a snapshot arrives); on failure it shows
// the error variant of the connecting screen.
func (a *App) pumpDial() {
	if a.dialCh == nil {
		return
	}
	select {
	case out := <-a.dialCh:
		a.dialCh = nil
		if out.err != nil {
			if a.dialCancel != nil { // tear down the orphaned host server
				a.dialCancel()
			}
			a.dialSrv, a.dialCancel = nil, nil
			a.netErr = friendlyDialErr(out.err)
			return
		}
		reAddr := a.pendingAddr // the address to redial on a reconnect (guest: the remote host)
		share := ""
		if out.isHost {
			reAddr = loopbackAddr(a.pendingAddr)
			share = lanAddress(defaultMPPort) // what friends type into Join
		}
		a.net = &netSession{
			client: out.client, isHost: out.isHost, srv: a.dialSrv, srvCancel: a.dialCancel,
			human: input.NewHuman(), addr: reAddr, share: share, name: a.netName(),
			token: out.client.SessionToken(), toast: latencyToastFrames,
		}
		a.dialSrv, a.dialCancel = nil, nil
		a.state = StateMPLobby
	default:
	}
}

// cancelConnect aborts an in-progress connect, tearing down the host server if one was started.
func (a *App) cancelConnect() {
	if a.dialCancel != nil {
		a.dialCancel()
	}
	a.dialSrv, a.dialCancel = nil, nil
	a.dialCh = nil
	a.netErr = ""
	if a.mpRole == roleHost {
		a.state = StateMatchSetup
	} else {
		a.state = StateMPJoin
	}
}

func friendlyDialErr(err error) string {
	msg := "Couldn't reach the host — check the address and that the host is running."
	if err != nil {
		msg += "\n(" + err.Error() + ")"
	}
	return msg
}

// leaveNetwork idempotently tears the session down: closes the client and cancels the in-proc
// server (leak-free via the client's closed-guard and the server's ctx-cancel teardown).
func (a *App) leaveNetwork() {
	if a.net != nil {
		if a.net.client != nil {
			a.net.client.Close()
		}
		if a.net.srvCancel != nil {
			a.net.srvCancel()
		}
		a.net = nil
	}
	a.dialCh = nil
	a.netErr = ""
	a.mpRole = roleNone
}

// updateMPLobby watches the lobby connection and moves into play once the host starts the match.
func (a *App) updateMPLobby() {
	if a.net == nil {
		return
	}
	c := a.net.client
	if down, err := c.ConnState(); down {
		a.onConnDown(err)
		return
	}
	if c.InMatch() { // host started (or we joined mid-match as a spectator)
		a.net.lastSoundTick = 0
		a.state = StatePlaying
	}
}

// updateMPResult watches the result connection and returns to the lobby when the host does.
func (a *App) updateMPResult() {
	if a.net == nil {
		return
	}
	c := a.net.client
	if down, err := c.ConnState(); down {
		a.onConnDown(err)
		return
	}
	if c.InLobby() { // host returned everyone to the lobby for another match
		a.state = StateMPLobby
	}
}

// updateMPPlaying is the networked counterpart of updatePlaying: it follows snapshots instead of
// stepping a local match. It watches connection health, routes lobby/finish transitions, sends the
// local intent, and plays this tick's sounds.
func (a *App) updateMPPlaying() {
	if a.net == nil {
		return
	}
	c := a.net.client
	if down, err := c.ConnState(); down {
		a.onConnDown(err)
		return
	}
	if c.InLobby() { // host returned everyone to the lobby
		a.state = StateMPLobby
		return
	}
	snap, ok := c.Snapshot()
	if ok && snap.Finished {
		a.state = StateResult
		return
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyP) {
		a.state = StatePaused
		return
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyTab) {
		a.statsHUD = !a.statsHUD
	}
	// Periodic latency ping (~1/s) for the badge.
	a.net.pingFrames++
	if a.net.pingFrames >= 60 {
		a.net.pingFrames = 0
		_ = c.Ping()
	}
	if a.net.toast > 0 {
		a.net.toast--
	}
	// Send the local intent (mapped through the last drawn viewport, like the offline path).
	a.net.human.SetViewport(a.worldViewport)
	_ = c.Send(a.net.human.Intent(nil))
	// Play this tick's sounds, de-duplicated by snapshot tick.
	if ok && a.audio != nil && snap.Tick > a.net.lastSoundTick {
		a.audio.Dispatch(snap.Sounds)
		a.net.lastSoundTick = snap.Tick
	}
}

// onConnDown routes a dropped connection: a friendly reason (reject/host-closed/version) goes to the
// connecting screen's error state; an unexplained drop (host crash, blip) goes to the reconnect
// screen, which redials with the resume token until a deadline.
func (a *App) onConnDown(err error) {
	reason := a.net.client.Reason()
	if reason != "" { // deliberate reject / host-closed: no point retrying
		a.netErr = reason
		a.leaveNetwork()
		a.state = StateMPConnecting
		return
	}
	if a.net.isHost { // our own server died: nothing to reconnect to
		msg := "the match ended unexpectedly"
		if err != nil {
			msg += "\n(" + err.Error() + ")"
		}
		a.netErr = msg
		a.leaveNetwork()
		a.state = StateMPConnecting
		return
	}
	a.net.deadline = time.Now().Add(netcode.ReconnectGrace)
	a.beginReconnect()
	a.state = StateMPReconnecting
}

// beginReconnect launches a background redial-with-resume loop until the deadline.
func (a *App) beginReconnect() {
	a.dialCh = make(chan dialOutcome, 1)
	addr, name, token := a.net.addr, a.net.name, a.net.token
	deadline := a.net.deadline
	ch := a.dialCh
	go func() {
		for time.Now().Before(deadline) {
			if c, err := netcode.DialResume(addr, name, token); err == nil {
				ch <- dialOutcome{client: c, resume: true}
				return
			}
			time.Sleep(400 * time.Millisecond)
		}
		ch <- dialOutcome{err: errReconnectTimeout}
	}()
}

// pumpReconnect polls the redial loop and either restores play or gives up to an error.
func (a *App) pumpReconnect() {
	if a.dialCh == nil || a.net == nil {
		return
	}
	select {
	case out := <-a.dialCh:
		a.dialCh = nil
		if out.err != nil {
			a.netErr = "Lost connection to the host."
			a.leaveNetwork()
			a.state = StateMPConnecting
			return
		}
		// Swap in the fresh connection; keep the rest of the session (and the server, if host).
		old := a.net.client
		a.net.client = out.client
		a.net.token = out.client.SessionToken()
		a.net.lastSoundTick = 0
		if old != nil {
			old.Close()
		}
		if a.net.client.InLobby() {
			a.state = StateMPLobby
		} else {
			a.state = StatePlaying
		}
	default:
	}
}

// adaptSnapshot projects the session's latest snapshot into a render.SnapshotView, marking the local
// player and surfacing latency. The adapter lives here so render never imports netcode.
func (a *App) adaptSnapshot(snap netcode.Snapshot) render.SnapshotView {
	ents := make([]render.SnapshotEntity, len(snap.Entities))
	for i, e := range snap.Entities {
		ents[i] = render.SnapshotEntity{
			IsBall:      e.Kind == netcode.KindBall,
			PlayerID:    e.PlayerID,
			Position:    e.Position,
			Facing:      e.Facing,
			Radius:      e.Radius,
			Color:       e.Color,
			Number:      e.Number,
			ShootCharge: e.ShootCharge,
			TrapCharge:  e.TrapCharge,
		}
	}
	selfID, haveSelf := a.net.client.AssignedID()
	if selfID < 0 {
		haveSelf = false // a spectator has no "you"
	}
	return render.SnapshotView{
		Geometry:             snap.Geometry,
		LeftName:             snap.LeftName,
		RightName:            snap.RightName,
		LeftColor:            snap.LeftColor,
		RightColor:           snap.RightColor,
		LeftScore:            snap.LeftScore,
		RightScore:           snap.RightScore,
		ClockSeconds:         snap.ClockSeconds,
		PhaseLabel:           snap.PhaseLabel,
		InShootout:           snap.InShootout,
		PenLeftGoals:         snap.PenLeftGoals,
		PenLeftTaken:         snap.PenLeftTaken,
		PenRightGoals:        snap.PenRightGoals,
		PenRightTaken:        snap.PenRightTaken,
		OffsideEnabled:       snap.OffsideEnabled,
		OffsideFrac:          snap.OffsideFrac,
		PenaltyBoxMaxPlayers: snap.PenaltyBoxMaxPlayers,
		GoalAreaMaxPlayers:   snap.GoalAreaMaxPlayers,
		Celebrating:          snap.Celebrating,
		GoalText:             snap.GoalText,
		WinnerText:           snap.WinnerText,
		Finished:             snap.Finished,
		Paused:               snap.Paused,
		GoalTint:             render.NeutralGoalTint,
		Entities:             ents,
		Stats:                snap.Stats,
		SelfPlayerID:         selfID,
		HaveSelf:             haveSelf,
		RTTms:                a.net.client.RTTms(),
	}
}
