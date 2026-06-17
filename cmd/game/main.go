// Command game runs phootball as a single local process: it owns the simulation,
// drives one player from the keyboard/mouse and the rest with AI, and renders.
package main

import (
	"flag"
	"log"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"phootball/internal/control"
	"phootball/internal/geom"
	"phootball/internal/input"
	"phootball/internal/render"
	"phootball/internal/sim"
)

const deltaTime = 1.0 / 60.0

// Game adapts the headless simulation to Ebiten: gather intents, step, render.
type Game struct {
	match       *sim.Match
	controllers map[int]control.Controller

	// Duo testing mode: the human controls one player at a time, switching with 1/2.
	duo      bool
	human    *input.Human
	activeID int
}

func (g *Game) Update() error {
	if g.duo {
		if inpututil.IsKeyJustPressed(ebiten.KeyDigit1) {
			g.activeID = 0
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyDigit2) {
			g.activeID = 1
		}
		// Only the active player receives input; the other stays idle (zero intent).
		g.match.Step(map[int]sim.Intent{g.activeID: g.human.Intent(g.match)}, deltaTime)
		return nil
	}

	inputs := make(map[int]sim.Intent, len(g.controllers))
	for id, c := range g.controllers {
		inputs[id] = c.Intent(g.match)
	}
	g.match.Step(inputs, deltaTime)
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) { render.Match(screen, g.match) }

// Layout renders the game at the display's physical pixel resolution so shapes stay
// crisp on high-DPI / 4K screens. The render package scales the fixed world to fill it.
func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	s := ebiten.DeviceScaleFactor()
	if s <= 0 {
		s = 1
	}
	return int(float64(outsideWidth) * s), int(float64(outsideHeight) * s)
}

func main() {
	teamSize := flag.Int("team-size", 3, "players per team")
	aiBoth := flag.Bool("ai-both", false, "AI controls both teams (spectate)")
	solo := flag.Bool("solo", false, "single human player + ball only, no opponents (for testing)")
	duo := flag.Bool("duo", false, "two players you switch control of with 1 and 2 (for testing)")
	flag.Parse()

	field := sim.NewStandardField()

	if *duo {
		ebiten.SetWindowSize(1200, 816)
		ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
		ebiten.SetWindowTitle("phootball (duo)")
		if err := ebiten.RunGame(&Game{match: sim.BuildDuo(field), duo: true, human: input.NewHuman()}); err != nil {
			log.Fatal(err)
		}
		return
	}

	var match *sim.Match
	controllers := make(map[int]control.Controller)
	if *solo {
		match = sim.BuildSolo(field)
		for _, p := range match.Players {
			controllers[p.PlayerID] = input.NewHuman()
		}
	} else {
		field.AddObstacle(sim.NewConeObstacle(geom.NewVec(field.CenterSpot.X, field.Min.Y+120), 14))
		field.AddObstacle(sim.NewConeObstacle(geom.NewVec(field.CenterSpot.X, field.Max.Y-120), 14))
		match = sim.BuildMatch(field, *teamSize)

		humanID := -1
		if !*aiBoth {
			humanID = humanSlot(match.Teams[0]) // control an outfielder on the blue team
		}
		for _, p := range match.Players {
			if p.PlayerID == humanID {
				controllers[p.PlayerID] = input.NewHuman()
			} else {
				controllers[p.PlayerID] = control.NewAI(p.PlayerID)
			}
		}
	}

	ebiten.SetWindowSize(1200, 816)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowTitle("phootball")
	if err := ebiten.RunGame(&Game{match: match, controllers: controllers}); err != nil {
		log.Fatal(err)
	}
}

// humanSlot picks an outfielder (the keeper is index 0) for the human to control.
func humanSlot(t *sim.Team) int {
	if len(t.Players) > 1 {
		return t.Players[1].PlayerID
	}
	return t.Players[0].PlayerID
}
