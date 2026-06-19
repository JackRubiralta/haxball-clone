package render

import (
	"image/color"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

// fxState holds the client-only celebration/stage animators. The timers advance by wall
// time (render is client-only and never linked into the headless server, so this does
// not affect simulation determinism). They are edge-triggered off the match's
// Celebrating()/Phase() so a rising edge restarts the animation.
type fxState struct {
	lastTick    time.Time
	wasCeleb    bool
	celebT      float64 // seconds since the goal celebration began (0 = none)
	lastPhase   sim.Phase
	phaseLabel0 string  // last observed snapshot phase label (client edge-detect)
	phaseT      float64 // seconds since the last stage change (counts up; card shown while < stageCardSeconds)
	phaseLabel  string  // the label to show on the stage card
	initialised bool
}

const (
	celebDuration    = 2.4 // goal overlay lifetime (seconds)
	stageCardSeconds = 2.0 // stage-card fade-in/out lifetime (seconds)
)

var fx fxState

// advanceFX ticks the cosmetic FX clock by the real elapsed time since the last draw.
func advanceFX() {
	now := time.Now()
	if fx.lastTick.IsZero() {
		fx.lastTick = now
		return
	}
	dt := now.Sub(fx.lastTick).Seconds()
	fx.lastTick = now
	if dt < 0 {
		dt = 0
	}
	if dt > 0.1 {
		dt = 0.1 // clamp a long stall so the animation does not jump
	}
	if fx.celebT > 0 {
		fx.celebT += dt
		if fx.celebT > celebDuration {
			fx.celebT = 0
		}
	}
	if fx.phaseT >= 0 && fx.phaseT < stageCardSeconds {
		fx.phaseT += dt
	}
}

// observe edge-detects the celebration and the phase so the FX timers restart on a rising
// edge. The phase that ENTERS a sudden-death/finished stage shows a card.
func (s *fxState) observe(celebrating bool, phase sim.Phase) {
	if celebrating && !s.wasCeleb {
		s.celebT = 0.0001 // arm
	}
	s.wasCeleb = celebrating
	if !s.initialised {
		s.lastPhase = phase
		s.initialised = true
		return
	}
	if phase != s.lastPhase {
		s.lastPhase = phase
		if label := stageCardLabel(phase); label != "" {
			s.phaseLabel = label
			s.phaseT = 0
		}
	}
}

// stageCardLabel returns the full-screen card text for a phase transition, or "" if that
// phase does not warrant a card.
func stageCardLabel(p sim.Phase) string {
	switch p {
	case sim.PhaseExtraTime:
		return "EXTRA TIME"
	case sim.PhaseGoldenGoal:
		return "GOLDEN GOAL — next goal wins"
	case sim.PhasePenalties:
		return "PENALTIES"
	case sim.PhaseFinished:
		return "FULL TIME"
	default:
		return ""
	}
}

// drawGoalOverlay draws the animated goal celebration: an expanding fading ring at the
// goal spot plus a scale-in/fade banner with the scorer/assist message, tinted to the
// scoring team. It is keyed off the client-side celebT timer.
func drawGoalOverlay(screen *ebiten.Image, message string, tint color.RGBA, ballPos geom.Vec, sw, sh float64) {
	if fx.celebT <= 0 {
		return
	}
	prog := fx.celebT / celebDuration
	if prog > 1 {
		prog = 1
	}
	// Expanding ring at the ball/goal spot. This mark is WORLD-ANCHORED -- it sits at the
	// ball's world position -- so it is drawn in the world box (newWorldFitCanvas) and scales
	// with the pitch like the ball does. (The banner below, by contrast, is screen-space.)
	worldW, worldH = sw, sh
	wc := newWorldFitCanvas(screen)
	ringR := 18 + prog*120
	alpha := uint8(200 * (1 - prog))
	const bands = 5
	for i := 0; i < bands; i++ {
		t := float64(i) / float64(bands)
		r := ringR * (0.5 + 0.5*t)
		a := uint8(float64(alpha) * (1 - t))
		if a == 0 {
			continue
		}
		wc.strokeCircle(ballPos.X, ballPos.Y, r, 3, fadeU8(tint, a)) // the team's normal colour (fades correctly)
	}

	// Scale-in / fade banner, centred. Fades out over the back third of the lifetime. The
	// banner is SCREEN-SPACE: it is drawn in the fixed overlay box so the "GOAL!" message is a
	// constant size on every pitch (unlike the world-anchored ring above, which scales).
	c := newOverlayCanvas(screen)
	bannerAlpha := 1.0
	if prog > 0.7 {
		bannerAlpha = 1 - (prog-0.7)/0.3
	}
	scaleIn := 1.0
	if prog < 0.18 {
		scaleIn = prog / 0.18
	}
	size := 22.0 * (0.6 + 0.4*scaleIn)
	cx, cy := overlayW/2, overlayH*0.34
	bw := measureUI(message, size) + 48
	bh := size + 22
	c.fillRect(cx-bw/2, cy-bh/2, bw, bh, color.RGBA{0, 0, 0, uint8(165 * bannerAlpha)}) // black (valid premult)
	c.fillRect(cx-bw/2, cy-bh/2, 6, bh, fade(tint, bannerAlpha))                        // team accent bar
	c.fillRect(cx+bw/2-6, cy-bh/2, 6, bh, fade(tint, bannerAlpha))
	c.textSized(message, cx, cy, size, AlignCenter, fade(color.RGBA{240, 244, 240, 255}, bannerAlpha))
}

// drawStageCard draws the brief full-screen transition card ("EXTRA TIME", etc.) keyed
// off the client-side phaseT timer, fading in then out over stageCardSeconds.
func drawStageCard(screen *ebiten.Image, _ sim.Phase) {
	if fx.phaseLabel == "" || fx.phaseT >= stageCardSeconds {
		return
	}
	t := fx.phaseT / stageCardSeconds // 0..1 across the card's life
	// Fade in over the first 25%, hold, fade out over the last 35%.
	alpha := 1.0
	switch {
	case t < 0.25:
		alpha = t / 0.25
	case t > 0.65:
		alpha = 1 - (t-0.65)/0.35
	}
	if alpha < 0 {
		alpha = 0
	}
	c := newOverlayCanvas(screen)
	c.fillRect(0, 0, overlayW, overlayH, color.RGBA{8, 12, 16, uint8(150 * alpha)})
	bandH := 96.0
	cy := overlayH / 2
	c.fillRect(0, cy-bandH/2, overlayW, bandH, color.RGBA{12, 18, 24, uint8(210 * alpha)})
	c.fillRect(0, cy-bandH/2, overlayW, 3, fadeU8(color.RGBA{255, 196, 90, 255}, uint8(220*alpha)))
	c.fillRect(0, cy+bandH/2-3, overlayW, 3, fadeU8(color.RGBA{255, 196, 90, 255}, uint8(220*alpha)))
	c.textSized(fx.phaseLabel, overlayW/2, cy, 30, AlignCenter, fade(color.RGBA{245, 240, 230, 255}, alpha))
}

// DrawClientOverlays drives the goal overlay and stage-card transitions for the network
// client, which has no *sim.Match. It derives the same edge-triggered FX from the
// snapshot's existing fields: celebrating, the phase label, the goal text, the scoring
// team's tint (resolved by the caller), and the ball position. finished/finalText draw
// the result banner. Mirrors drawMatchOverlays for the local path.
func DrawClientOverlays(screen *ebiten.Image, celebrating bool, phaseLabel, goalText string,
	tint color.RGBA, ballPos geom.Vec, sw, sh float64, finished bool, finalText string) {
	advanceFX()
	fx.observeLabels(celebrating, phaseLabel)
	drawStageCard(screen, sim.PhasePlaying)
	if celebrating {
		msg := goalText
		if msg == "" {
			msg = "G O A L !"
		}
		drawGoalOverlay(screen, msg, tint, ballPos, sw, sh)
	}
	if finished {
		centerBanner(screen, finalText)
	}
}

// observeLabels is the snapshot-driven counterpart to observe: it edge-detects the
// celebration and the phase by its label string (the client has no sim.Phase).
func (s *fxState) observeLabels(celebrating bool, phaseLabel string) {
	if celebrating && !s.wasCeleb {
		s.celebT = 0.0001
	}
	s.wasCeleb = celebrating
	if !s.initialised {
		s.phaseLabel0 = phaseLabel
		s.initialised = true
		return
	}
	if phaseLabel != s.phaseLabel0 {
		s.phaseLabel0 = phaseLabel
		if label := stageCardLabelFor(phaseLabel); label != "" {
			s.phaseLabel = label
			s.phaseT = 0
		}
	}
}

// stageCardLabelFor maps a snapshot phase label to a stage-card message.
func stageCardLabelFor(phaseLabel string) string {
	switch phaseLabel {
	case "EXTRA TIME":
		return "EXTRA TIME"
	case "GOLDEN GOAL":
		return "GOLDEN GOAL — next goal wins"
	case "PENALTIES":
		return "PENALTIES"
	case "FULL TIME":
		return "FULL TIME"
	default:
		return ""
	}
}

// NeutralGoalTint is the neutral celebration tint used when the scoring side is unknown -- a
// network client whose snapshot carries no scoring side. One definition shared by the local
// goalTint default and the network clients (cmd/client, menu).
var NeutralGoalTint = color.RGBA{240, 244, 240, 255}

// goalTint returns the scoring team's colour for the goal overlay, defaulting to the neutral tint.
func goalTint(m *sim.Match) color.RGBA {
	if g := m.LastGoal; g != nil {
		if m.Teams[0].Side == g.Team {
			return m.Teams[0].Color
		}
		return m.Teams[1].Color
	}
	return NeutralGoalTint
}

// centerBanner draws a centred overlay message (goal, pause, result).
func centerBanner(screen *ebiten.Image, message string) {
	c := newOverlayCanvas(screen)
	c.fillRect(0, overlayH/2-26, overlayW, 52, bannerColor)
	c.textSized(message, overlayW/2, overlayH/2, 24, AlignCenter, hudText)
}
