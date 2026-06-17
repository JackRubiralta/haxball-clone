// Command server runs the authoritative, headless simulation for LAN play. It links
// only sim/physics (no Ebiten): it steps the one true match, fills every slot with
// AI until clients connect, and broadcasts snapshots.
package main

import (
	"flag"
	"log"

	"phootball/internal/control"
	"phootball/internal/netcode"
	"phootball/internal/sim"
)

func main() {
	addr := flag.String("addr", ":4000", "listen address")
	teamSize := flag.Int("team-size", 3, "players per team")
	flag.Parse()

	field := sim.NewStandardField()
	match := sim.BuildMatch(field, *teamSize)

	// One claimable human slot per team (an outfielder if there is one); every
	// player also has an AI fallback that runs until a client claims its slot.
	humanIDs := make([]int, 0, 2)
	for _, t := range match.Teams {
		idx := 0
		if len(t.Players) > 1 {
			idx = 1
		}
		humanIDs = append(humanIDs, t.Players[idx].PlayerID)
	}

	bots := make(map[int]netcode.Bot, len(match.Players))
	for _, p := range match.Players {
		bots[p.PlayerID] = control.NewAI(p.PlayerID)
	}

	server := netcode.NewServer(*addr, match, bots, humanIDs)
	log.Fatal(server.Run())
}
