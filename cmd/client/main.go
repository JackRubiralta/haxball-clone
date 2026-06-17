// Command client is a thin Ebiten front end for LAN play: it sends the local
// player's intent to the server and renders the latest authoritative snapshot. It
// runs no gameplay collisions.
package main

import (
	"flag"
	"image/color"
	"log"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"

	"phootball/internal/input"
	"phootball/internal/netcode"
	"phootball/internal/render"
	"phootball/internal/sim"
)

// Game sends intents and draws server snapshots.
type Game struct {
	client *netcode.Client
	human  *input.Human
}

func (g *Game) Update() error {
	return g.client.Send(g.human.Intent(nil))
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.White)
	snap, ok := g.client.Snapshot()
	if !ok {
		ebitenutil.DebugPrint(screen, "connecting to server...")
		return
	}

	field := sim.NewField(snap.FieldMin, snap.FieldMax, snap.GoalWidth, snap.GoalHeight)
	render.Field(screen, field, snap.LeftColor, snap.RightColor)
	for _, e := range snap.Entities {
		if e.Kind == netcode.KindBall {
			render.BallAt(screen, e.Position, e.Radius)
		} else {
			render.PlayerAt(screen, e.Position, e.Facing, e.Radius, e.Color, e.Number, e.ShootCharge, e.TrapCharge)
		}
	}
	render.Scoreboard(screen, snap.LeftName, snap.LeftScore, snap.RightName, snap.RightScore)
	if snap.Celebrating {
		render.GoalBanner(screen)
	}
}

// Layout renders at the display's physical pixel resolution so shapes stay crisp on
// high-DPI / 4K screens. The render package scales the fixed world to fill it.
func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	s := ebiten.DeviceScaleFactor()
	if s <= 0 {
		s = 1
	}
	return int(float64(outsideWidth) * s), int(float64(outsideHeight) * s)
}

func main() {
	addr := flag.String("addr", "localhost:4000", "server address")
	flag.Parse()

	client, err := netcode.Dial(*addr)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ebiten.SetWindowSize(1200, 816)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowTitle("phootball (client)")
	if err := ebiten.RunGame(&Game{client: client, human: input.NewHuman()}); err != nil {
		log.Fatal(err)
	}
}
