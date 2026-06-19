// Command diag inspects HOW the neural controller actually plays vs an opponent: it breaks each
// team's goals into (real goals scored by that team's players) vs (own-goals gifted by the other
// team), reports shots/possession, and prints the goal-event log of one match. This exposes
// gameplay quality that an aggregate win-rate can hide (e.g. "winning" only via opponent
// own-goals, or scoring with zero shots).
package main

import (
	"flag"
	"fmt"
	"os"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/control/neural"
	"phootball/internal/eval"
	"phootball/internal/policy"
	"phootball/internal/sim"
)

func main() {
	weights := flag.String("weights", "", "neural weights (empty=embedded)")
	sizesSpec := flag.String("sizes", "1,2,3,4,5", "team sizes")
	field := flag.String("field", "medium", "field preset")
	opp := flag.String("opp", "impossible", "opponent tier")
	leftSkill := flag.String("leftskill", "", "if set, LEFT side is a rule AI at this skill (reference) instead of neural")
	seeds := flag.Int("seeds", 12, "matches per size")
	ticks := flag.Int("ticks", 3600, "ticks/match")
	flag.Parse()

	var net *policy.Net
	var err error
	if *weights == "" {
		net, err = policy.LoadDefault()
	} else {
		f, e := os.Open(*weights)
		if e != nil {
			panic(e)
		}
		net, err = policy.Load(f)
		f.Close()
	}
	if err != nil {
		panic(err)
	}
	geom, _ := config.PresetByName(*field)
	oppSkill, _ := control.SkillFromString(*opp)

	fmt.Printf("weights=%s vs %s, field=%s, %d seeds/size, %ds\n",
		func() string {
			if *weights == "" {
				return "embedded"
			}
			return *weights
		}(), *opp, *field, *seeds, *ticks/60)

	for _, size := range parseSizes(*sizesSpec) {
		var nnTeamG, nnPlayerG, nnOwn, nnShots, nnSOT float64
		var aiTeamG, aiPlayerG, aiOwn float64
		var nnPoss float64
		var nnWins, aiWins, draws int
		dumped := false
		for s := 0; s < *seeds; s++ {
			seed := int64(s)
			m := eval.BuildWith(size, seed, func(c *config.Config) { c.Geometry = geom },
				func(id int, side sim.Side) control.Controller {
					if side == sim.SideLeft {
						if *leftSkill != "" {
							ls, _ := control.SkillFromString(*leftSkill)
							return control.NewAISkill(id, ls)
						}
						return neural.New(id, net)
					}
					return control.NewAISkill(id, oppSkill)
				})
			m.M.EnableRecording()
			m.Run(*ticks, nil)
			st := m.M.Stats()
			nn, ai := teamOf(st, sim.SideLeft), teamOf(st, sim.SideRight)
			nnTeamG += float64(nn.Goals)
			aiTeamG += float64(ai.Goals)
			nnShots += float64(nn.Shots)
			nnSOT += float64(nn.ShotsOnTarget)
			nnPoss += poss(nn, ai)
			pg, og := playerGoals(st, sim.SideLeft)
			nnPlayerG += float64(pg)
			nnOwn += float64(og)
			apg, aog := playerGoals(st, sim.SideRight)
			aiPlayerG += float64(apg)
			aiOwn += float64(aog)
			switch {
			case nn.Goals > ai.Goals:
				nnWins++
			case nn.Goals < ai.Goals:
				aiWins++
			default:
				draws++
			}
			if !dumped {
				dumped = true
				dumpGoals(size, seed, st)
			}
		}
		n := float64(*seeds)
		fmt.Printf("\nSIZE %d vs %s: NN wins %d / AI wins %d / draws %d\n", size, *opp, nnWins, aiWins, draws)
		fmt.Printf("  NN goals/match: team=%.2f  (by NN players=%.2f, via AI own-goals=%.2f)  shots=%.2f onTarget=%.2f\n",
			nnTeamG/n, nnPlayerG/n, nnOwn/n, nnShots/n, nnSOT/n)
		fmt.Printf("  AI goals/match: team=%.2f  (by AI players=%.2f, via NN own-goals=%.2f)\n",
			aiTeamG/n, aiPlayerG/n, aiOwn/n)
		fmt.Printf("  NN possession: %.1f%%\n", 100*nnPoss/n)
	}
}

func dumpGoals(size int, seed int64, st sim.MatchStats) {
	fmt.Printf("  [size %d seed %d goal events]:", size, seed)
	cnt := 0
	for _, e := range st.Events {
		if e.Kind == sim.EvGoal || e.Kind == sim.EvOwnGoal {
			kind := "GOAL"
			if e.Kind == sim.EvOwnGoal {
				kind = "OWNGOAL"
			}
			fmt.Printf(" [t%d %s creditSide=%d player=%d]", e.Tick, kind, e.Team, e.Player)
			cnt++
		}
	}
	if cnt == 0 {
		fmt.Printf(" (none)")
	}
	fmt.Println()
}

func teamOf(st sim.MatchStats, side sim.Side) *sim.TeamStat {
	for i := range st.Teams {
		if st.Teams[i].Side == side {
			return &st.Teams[i]
		}
	}
	return &sim.TeamStat{}
}

// playerGoals returns (sum of real goals by players on side, sum of own-goals by players on side).
func playerGoals(st sim.MatchStats, side sim.Side) (goals, own int) {
	for i := range st.Players {
		if st.Players[i].Side == side {
			goals += st.Players[i].Goals
			own += st.Players[i].OwnGoals
		}
	}
	return
}

func poss(a, b *sim.TeamStat) float64 {
	tot := a.PossessionSeconds + b.PossessionSeconds
	if tot <= 0 {
		return 0
	}
	return a.PossessionSeconds / tot
}

func parseSizes(s string) []int {
	var out []int
	cur := 0
	have := false
	for _, c := range s + "," {
		if c >= '0' && c <= '9' {
			cur = cur*10 + int(c-'0')
			have = true
		} else if c == ',' && have {
			out = append(out, cur)
			cur, have = 0, false
		}
	}
	return out
}
