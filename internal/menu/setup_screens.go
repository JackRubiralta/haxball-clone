package menu

import (
	"math"
	"strconv"

	"github.com/hajimehoshi/ebiten/v2"

	"phootball/internal/config"
	"phootball/internal/render"
)

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
		}
		// The difficulty governs this side's AI players: the whole side when it's AI-controlled, or the
		// human's AI team-mates when it's human-controlled. So show it whenever the side has at least one
		// AI player -- always for an AI side, and for a human side with more than the single human slot --
		// and hide it only for a lone human with no team-mates to govern.
		if !tc.Human || tc.Size > 1 {
			diff := indexOf(difficultyPresets, tc.Difficulty)
			// No row label: the controller options (algo/neural) use the full half-column width. The
			// "Control"/"Human slot" rows above already establish whose controller this is.
			if sel := f.segmented("", difficultyPresets, diff, c.x, c.row(), c.w); sel != diff {
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
	a.tuneRow(f, col, "Energy drain /s", &p.TrapDrainPerSecond, 0.1, 5, 0.05, tfDec2)
	a.tuneRow(f, col, "Energy regen /s", &p.TrapRegenPerSecond, 0.05, 5, 0.05, tfDec2)
	a.tuneRow(f, col, "Aura rate /s", &p.TrapAuraRatePerSecond, 0.5, 20, 0.5, tfDec1)
	a.tuneRow(f, col, "Min aura (drained)", &p.TrapMinAura, 0, 0.5, 0.01, tfDec2)

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

	// Movement model (bottom of the tab). Default "Standard" is the current omnidirectional feel;
	// "Directional" makes speed depend on facing (fast toward your aim, slow backward). When
	// directional, a second toggle picks the WASD scheme: "Strafe" keeps world-absolute keys,
	// "Locked" makes WASD relative to your aim (W = toward the cursor).
	f.sectionHeader("MOVEMENT", col.x, col.header(1), col.w)
	modelSel := 0
	if t.MoveModel != config.MoveStandard {
		modelSel = 1
	}
	if sel := f.segmented("Speed model", []string{"Standard", "Directional"}, modelSel, col.x, col.row(), col.w); sel != modelSel {
		if sel == 0 {
			t.MoveModel = config.MoveStandard
		} else {
			t.MoveModel = config.MoveDirectional // enable: default to the strafe (world-absolute) scheme
		}
	}
	if t.MoveModel != config.MoveStandard {
		schemeSel := 0
		if t.MoveModel == config.MoveDirectionalLocked {
			schemeSel = 1
		}
		if sel := f.segmented("WASD", []string{"Strafe", "Locked"}, schemeSel, col.x, col.row(), col.w); sel != schemeSel {
			if sel == 0 {
				t.MoveModel = config.MoveDirectional
			} else {
				t.MoveModel = config.MoveDirectionalLocked
			}
		}
		// Per-direction speed multipliers (forward toward the aim is fastest). Only relevant under a
		// directional model, so shown alongside the scheme toggle.
		a.tuneRow(f, col, "Forward speed", &t.MoveForward, 0.1, 1.5, 0.05, tfDec2)
		a.tuneRow(f, col, "Side speed", &t.MoveSide, 0.1, 1.5, 0.05, tfDec2)
		a.tuneRow(f, col, "Back speed", &t.MoveBack, 0.1, 1.5, 0.05, tfDec2)
	}
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
