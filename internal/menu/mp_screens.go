package menu

import (
	"image/color"
	"net"
	"strconv"
	"strings"
	"time"

	"phootball/internal/netcode"
	"phootball/internal/render"
)

// lobbyTeamColor are the swatch colours for the lobby team columns (the sim's blue/red); the live
// in-match colours come exactly from the snapshot.
var lobbyTeamColor = [2]color.RGBA{{80, 140, 255, 255}, {255, 100, 100, 255}}

// --- Home: host / join chooser ---------------------------------------------------------------

func (a *App) screenMPHome(f frame) {
	if f.draw {
		f.ui.Fill(theme.BG)
		f.ui.Title("MULTIPLAYER", render.UIWidth/2, 150, theme.Title, theme.Accent)
		f.ui.TextCenteredS("LAN — host a match, or join one on your network",
			render.UIWidth/2, 200, theme.Body, theme.TextDim)
	}
	bw, bh := 280.0, theme.BtnH
	bx := render.UIWidth/2 - bw/2
	y := 280.0
	if f.button("Host Game", bx, y, bw, bh) {
		a.mpRole = roleHost
		a.editingLobby = false
		a.state = StateMatchSetup
	}
	y += bh + 18
	if f.button("Join Game", bx, y, bw, bh) {
		if a.mpAddr == "" {
			a.mpAddr = defaultMPDialAddr
		}
		a.focusField = nil
		a.state = StateMPJoin
	}
	y += bh + 18
	if f.button("Back", bx, y, bw, bh) {
		a.mpRole = roleNone
		a.state = StateMenu
	}
}

// --- Join: address entry ----------------------------------------------------------------------

func (a *App) screenMPJoin(f frame) {
	x, y0, w := f.backdrop("JOIN GAME")
	if a.mpName == "" {
		a.mpName = "Player"
	}
	if a.mpAddr == "" {
		a.mpAddr = defaultMPDialAddr
	}
	caretOn := (a.caretBlink/30)%2 == 0
	col := newCol(x, y0, w)

	col.gapRow(0.2)
	if f.rowTextField("Name", &a.mpName, col.x, col.row(), col.w, a.focusField == &a.mpName, caretOn, acceptName, 24) {
		a.focusField = &a.mpName
	}
	if f.rowTextField("Server", &a.mpAddr, col.x, col.row(), col.w, a.focusField == &a.mpAddr, caretOn, acceptAddr, 48) {
		a.focusField = &a.mpAddr
	}

	if len(a.recentAddrs) > 0 {
		col.gapRow(0.3)
		f.sectionHeader("RECENT", col.x, col.header(1), col.w)
		for _, ad := range a.recentAddrs {
			if f.selectButton(ad, ad == a.mpAddr, col.x, col.row(), col.w, theme.RowH-10) {
				a.mpAddr = ad
			}
		}
	}

	const barH = 52.0
	barY := 70.0 + 560.0 - theme.PanelPad - barH // backdrop panel is py=70, ph=560
	cx := render.UIWidth / 2.0
	if f.button("Back", cx-200, barY, 180, barH) {
		a.focusField = nil
		a.state = StateMPHome
	}
	if validAddr(a.mpAddr) {
		if f.button("Connect", cx+20, barY, 180, barH) {
			a.focusField = nil
			a.rememberAddr(a.mpAddr)
			a.beginGuest(a.mpAddr)
		}
	} else if f.draw {
		f.ui.FillRect(cx+20, barY, 180, barH, theme.BtnBG)
		f.ui.StrokeRect(cx+20, barY, 180, barH, 2, theme.Edge)
		f.ui.TextCenteredS("Connect", cx+20+90, barY+barH/2, theme.Body, theme.TextDim)
	}
}

// --- Connecting (and its error variant) -------------------------------------------------------

func (a *App) screenMPConnecting(f frame) {
	cx := render.UIWidth / 2.0
	if f.draw {
		f.ui.Fill(theme.BG)
	}
	if a.netErr != "" { // error variant
		if f.draw {
			f.ui.Title("CONNECTION FAILED", cx, 170, theme.H1, theme.Bad)
			drawCentredLines(f, a.netErr, cx, 234, theme.Body, theme.TextDim)
		}
		bw, bh := 200.0, theme.BtnH
		y := 420.0
		if f.button("Retry", cx-bw-12, y, bw, bh) {
			a.netErr = ""
			if a.mpRole == roleHost {
				a.beginHost(defaultMPListenAddr)
			} else {
				a.beginGuest(a.pendingAddr)
			}
		}
		if f.button("Back", cx+12, y, bw, bh) {
			a.netErr = ""
			if a.mpRole == roleHost {
				a.state = StateMatchSetup
			} else {
				a.state = StateMPJoin
			}
		}
		return
	}
	if f.draw {
		f.ui.Title("CONNECTING", cx, 200, theme.H1, theme.Accent)
		dots := strings.Repeat(".", 1+(a.caretBlink/20)%3)
		f.ui.TextCenteredS("Reaching "+a.pendingAddr+dots, cx, 264, theme.Body, theme.TextDim)
	}
	if f.button("Cancel", cx-90, 360, 180, theme.BtnH) {
		a.cancelConnect()
	}
}

// --- Reconnecting overlay (over the frozen last frame) ----------------------------------------

func (a *App) screenMPReconnecting(f frame) {
	cx := render.UIWidth / 2.0
	if f.draw {
		f.ui.DimScreen(theme.Overlay)
		f.ui.Title("RECONNECTING", cx, 250, theme.H1, theme.Accent)
		secs := 0
		if a.net != nil {
			if s := int(time.Until(a.net.deadline).Seconds()) + 1; s > 0 {
				secs = s
			}
		}
		f.ui.TextCenteredS("Lost connection — retrying ("+strconv.Itoa(secs)+"s)…",
			cx, 304, theme.Body, theme.TextDim)
	}
	if f.button("Leave", cx-90, 364, 180, theme.BtnH) {
		a.quitToMenu()
	}
}

// --- Lobby ------------------------------------------------------------------------------------

func (a *App) screenMPLobby(f frame) {
	if a.net == nil {
		return
	}
	const px, py, pw, ph = 40.0, 36.0, 920.0, 608.0
	const barH = 56.0
	pad := theme.PanelPad
	if f.draw {
		f.ui.Fill(theme.BG)
		f.ui.Panel(px, py, pw, ph, theme.Panel, theme.Edge)
		f.ui.Title("LOBBY", render.UIWidth/2, py+26, theme.H1, theme.Accent)
	}
	c := a.net.client
	lobby, ok := c.Lobby()
	selfID, _ := c.AssignedID()
	isHost := c.IsHost()

	if f.draw {
		summaryY := py + 28
		if isHost && a.net.share != "" { // tell the host the address others type into Join
			f.ui.TextS("Friends join at:  "+a.net.share, px+pad, summaryY, theme.Body, theme.Accent)
			summaryY += 22
		}
		if ok {
			f.ui.TextS(lobby.ConfigSummary+"  ·  "+roleTag(isHost), px+pad, summaryY, theme.Small, theme.TextDim)
		}
	}
	// Disconnect: leave the whole session back to the main menu (the host tells everyone first). This
	// is the real "shut it down"; in-match the host instead uses Return to Lobby to end a game.
	if f.button("Disconnect", px+pw-pad-130, py+14, 130, 34) {
		if isHost {
			a.net.client.HostClose()
		}
		a.quitToMenu()
	}

	colTop := py + 80
	colW := (pw - 3*pad) / 2
	homeX := px + pad
	awayX := homeX + colW + pad
	barY := py + ph - pad - barH

	if ok {
		a.drawLobbyColumn(f, lobby, 0, "BLUE", homeX, colTop, colW, selfID, isHost)
		a.drawLobbyColumn(f, lobby, 1, "RED", awayX, colTop, colW, selfID, isHost)
		if f.draw {
			if len(lobby.Spectators) > 0 {
				f.ui.TextS("Spectators: "+joinNames(lobby.Spectators), homeX, barY-26, theme.Small, theme.TextDim)
			}
			f.ui.TextRightS(latencyLabel(c.RTTms()), px+pw-pad, barY-26, theme.Small, latencyColor(c.RTTms()))
		}
	}

	// Self controls: Join-as segmented + Ready toggle.
	seg := selfSegment(selfID, lobby)
	const segW = 300.0
	if sel := f.segmented("Join as", []string{"Spectate", "Blue", "Red"}, seg, homeX, barY, segW); sel != seg {
		switch sel {
		case 0:
			c.PickSlot(-1, 0)
		case 1:
			c.PickSlot(0, -1) // first open on home
		case 2:
			c.PickSlot(1, -1) // first open on away
		}
	}
	if selfID >= 0 {
		ready := selfReady(selfID, lobby)
		if f.button(readyLabel(ready), homeX+segW+16, barY, 130, barH) {
			c.SetReady(!ready)
		}
	}

	// Host / guest controls (right).
	if isHost {
		startX := px + pw - pad - 180
		editX := startX - 160 - 12
		if f.button("Edit Settings", editX, barY, 160, barH) {
			a.mpRole = roleHost
			a.editingLobby = true
			a.state = StateMatchSetup
		}
		label := "Start Match"
		if ok && !lobby.AllReady {
			label = "Start (fill AI)"
		}
		if f.button(label, startX, barY, 180, barH) {
			c.StartMatch()
		}
	} else if f.draw {
		f.ui.TextRightS("Waiting for the host to start…", px+pw-pad, barY+barH/2, theme.Body, theme.TextDim)
	}
}

func (a *App) drawLobbyColumn(f frame, lobby netcode.LobbyState, team int, name string, x, y, w float64, selfID int, isHost bool) {
	col := newCol(x, y, w)
	hy := col.row()
	if f.draw {
		render.TeamSwatch(f.screen, x+10, hy+theme.RowH/2, 20, lobbyTeamColor[team])
		f.ui.TextS(name, x+32, hy+theme.RowH/2, theme.Body, lobbyTeamColor[team])
	}
	for _, seat := range lobby.Seats {
		if seat.Team != team {
			continue
		}
		a.drawSeatRow(f, seat, x, col.row(), w, selfID, isHost)
	}
}

func (a *App) drawSeatRow(f frame, seat netcode.SeatInfo, x, y, w float64, selfID int, isHost bool) {
	mid := y + theme.RowH/2
	you := seat.IsHuman && seat.PlayerID == selfID
	bw, bh := 78.0, theme.RowH-12
	btnX := x + w - bw

	if f.draw {
		f.ui.TextS(strconv.Itoa(seat.Slot+1)+" · "+seat.Role, x, mid, theme.Small, theme.TextDim)
		occ, occColor := "— open —", theme.TextDim
		if seat.IsHuman {
			occ, occColor = seat.OccupantName, theme.Text
			if you {
				occ, occColor = occ+"  (you)", theme.Accent
			}
		}
		f.ui.TextS(occ, x+56, mid, theme.Body, occColor)
		if seat.IsHuman { // ready pip, left of the button
			pipX := btnX - 22
			if seat.Ready {
				f.ui.FillRect(pipX, mid-5, 10, 10, theme.Accent)
			} else {
				f.ui.StrokeRect(pipX, mid-5, 10, 10, 2, theme.Edge)
			}
		}
	}
	switch {
	case !seat.IsHuman: // open seat: claim it
		if f.button("Take", btnX, y, bw, bh) {
			a.net.client.PickSlot(seat.Team, seat.Slot)
		}
	case you: // your seat: vacate to spectator
		if f.button("Leave", btnX, y, bw, bh) {
			a.net.client.PickSlot(-1, 0)
		}
	case isHost: // another human's seat: host may kick
		if f.button("Kick", btnX, y, bw, bh) {
			a.net.client.Kick(seat.PlayerID)
		}
	}
}

// --- Networked result -------------------------------------------------------------------------

func (a *App) screenMPResult(f frame) {
	if a.net == nil {
		return
	}
	snap, _ := a.net.client.Snapshot()
	r := buildResultFromSnapshot(snap)
	const px, py, pw, ph = 110.0, 60.0, 780.0, 540.0
	const barH = 52.0
	pad := theme.PanelPad
	if f.draw {
		f.ui.DimScreen(resultOverlay)
		f.ui.Panel(px, py, pw, ph, theme.Panel, theme.Edge)
	}
	innerX := px + pad
	innerW := pw - 2*pad
	a.drawResultHeader(f, r, innerX, py+24, innerW)
	if f.draw {
		sm := render.StatsModelFromStats(snap.Stats, snap.LeftName, snap.RightName, snap.LeftColor, snap.RightColor)
		drawMPResultStats(f, sm, innerX, py+208, innerW)
	}

	barY := py + ph - pad - barH
	bw := 200.0
	if a.net.client.IsHost() {
		bx := innerX + (innerW-(2*bw+24))/2
		if f.button("Back to Lobby", bx, barY, bw, barH) {
			a.net.client.ReturnToLobby()
		}
		if f.button("End Match", bx+bw+24, barY, bw, barH) {
			a.net.client.HostClose()
			a.quitToMenu()
		}
	} else {
		if f.button("Leave", innerX+(innerW-bw)/2, barY, bw, barH) {
			a.quitToMenu()
		}
	}
}

// drawMPResultStats renders the final team stat sheet from a snapshot-derived StatsModel (no
// *sim.Match on the networked path).
func drawMPResultStats(f frame, sm render.StatsModel, x, y, w float64) {
	f.ui.TextS("STATS", x, y, theme.Section, theme.Accent)
	f.ui.Line(x, y+8, x+w, y+8, 1, theme.Edge)
	homeX := x + w*0.62
	awayX := x + w
	rows := []struct{ label, home, away string }{
		{"Possession", resPct(sm.Left.PossessionPct), resPct(sm.Right.PossessionPct)},
		{"Shots (on tgt)", resShots(sm.Left.Shots, sm.Left.OnTarget), resShots(sm.Right.Shots, sm.Right.OnTarget)},
		{"Passes", resPasses(sm.Left.PassesDone, sm.Left.Passes), resPasses(sm.Right.PassesDone, sm.Right.Passes)},
		{"Interceptions", strconv.Itoa(sm.Left.Interceptions), strconv.Itoa(sm.Right.Interceptions)},
		{"Saves", strconv.Itoa(sm.Left.Saves), strconv.Itoa(sm.Right.Saves)},
	}
	ry := y + 34
	for _, r := range rows {
		f.ui.TextS(r.label, x, ry, theme.Body, theme.Text)
		f.ui.TextCenteredS(r.home, homeX, ry, theme.Body, theme.Text)
		f.ui.TextRightS(r.away, awayX, ry, theme.Body, theme.Text)
		ry += theme.RowH
	}
}

// --- helpers ----------------------------------------------------------------------------------

// acceptName accepts a printable, non-control rune for a display name.
func acceptName(r rune) bool { return r >= 0x20 && r != 0x7f }

// acceptAddr accepts the characters of a host:port (letters, digits, dot, colon, hyphen).
func acceptAddr(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == '.' || r == ':' || r == '-'
}

// validAddr reports whether s parses as host:port with a non-empty port (a cheap syntactic check,
// not a live probe).
func validAddr(s string) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(s))
	return err == nil && host != "" && port != ""
}

// rememberAddr pushes addr to the front of the recents (deduped, capped).
func (a *App) rememberAddr(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	out := []string{addr}
	for _, ad := range a.recentAddrs {
		if ad != addr && len(out) < 4 {
			out = append(out, ad)
		}
	}
	a.recentAddrs = out
	a.saveUserConfig()
}

func roleTag(isHost bool) string {
	if isHost {
		return "[HOST]"
	}
	return "[GUEST]"
}

func joinNames(names []string) string { return strings.Join(names, ", ") }

func latencyLabel(rtt int) string {
	if rtt <= 0 {
		return "ping —"
	}
	return "ping " + strconv.Itoa(rtt) + "ms"
}

// latencyColor grades a round-trip time: green (good) / amber (noticeable) / red (laggy).
func latencyColor(rtt int) color.RGBA {
	switch {
	case rtt <= 0:
		return theme.TextDim
	case rtt < 60:
		return color.RGBA{120, 220, 120, 255}
	case rtt < 120:
		return color.RGBA{230, 200, 110, 255}
	default:
		return theme.Bad
	}
}

func readyLabel(ready bool) string {
	if ready {
		return "Ready ✓"
	}
	return "Ready?"
}

// selfSegment returns the "Join as" index (0 spectate, 1 blue, 2 red) for the local client.
func selfSegment(selfID int, lobby netcode.LobbyState) int {
	if selfID < 0 {
		return 0
	}
	for _, s := range lobby.Seats {
		if s.PlayerID == selfID {
			return s.Team + 1
		}
	}
	return 0
}

func selfReady(selfID int, lobby netcode.LobbyState) bool {
	for _, s := range lobby.Seats {
		if s.PlayerID == selfID {
			return s.Ready
		}
	}
	return false
}

// drawCentredLines draws a '\n'-separated message centred under y.
func drawCentredLines(f frame, msg string, cx, y, size float64, clr color.RGBA) {
	for _, line := range strings.Split(msg, "\n") {
		f.ui.TextCenteredS(line, cx, y, size, clr)
		y += size * 1.5
	}
}
