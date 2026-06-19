// Command datagen generates Behavioral-Cloning / DAgger training shards for the neural
// controller. It rolls out rule-AI self-play (SkillHard/SkillImpossible) and, for every
// controlled player each tick, writes a Go-featurized observation plus the teacher's Intent
// discretized into the factored action heads. Go owns the featurization (internal/control/neural),
// so the shards are zero-skew with the runtime controller; Python only consumes the float32/int32
// arrays. It is pure-Go and deterministic given the seeds.
//
// One process generates one config slice (team size, field, ruleset, skill, seed range) into one
// self-describing shard file; a Workflow fans many processes over the coverage matrix.
//
// Shard binary layout (little-endian):
//
//	64-byte header:
//	  [0:8]   magic "PHBLDAT1"
//	  [8:12]  uint32 format version (=1)
//	  [12:16] uint32 feature dim (== neural.FlatDim)
//	  [16]    uint8  num heads
//	  [17:25] uint8  head sizes (first numHeads used)
//	  [25:32] pad
//	  [32:40] uint64 record count
//	  [40:44] uint32 record stride (= featureDim*4 + numHeads*4)
//	  [44:52] int64  seed (range start)
//	  [52:56] uint32 team size left
//	  [56:60] uint32 team size right
//	  [60:64] uint32 flags (bit0 offside, bit1 boxcaps, bits2-3 field preset 0=small 1=medium 2=large)
//	records: feature dim x float32 (obs), then numHeads x int32 (head labels)
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"log"
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/control/neural"
	"phootball/internal/policy"
	"phootball/internal/sim"
)

var le = binary.LittleEndian

const dt = 1.0 / 60.0

func buildMatch(size int, seed int64, mutate func(*config.Config)) *sim.Match {
	cfg := config.Default()
	cfg.Seed = seed
	if mutate != nil {
		mutate(&cfg)
	}
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	return sim.BuildMatchFromConfig(field, size, cfg)
}

func main() {
	log.SetFlags(0)
	var (
		size      = flag.Int("size", 4, "players per team (1..11)")
		field     = flag.String("field", "medium", "field preset: small|medium|large")
		offside   = flag.Bool("offside", false, "enable the offside rule")
		boxcaps   = flag.Bool("boxcaps", false, "enable penalty/goal-area player caps")
		skillName = flag.String("skill", "impossible", "teacher (label) skill: hard|impossible")
		actorName = flag.String("actor", "teacher", "who drives the match: teacher|neural (neural = DAgger; labels still come from the teacher)")
		weights   = flag.String("weights", "", "neural actor weights file (required with -actor neural)")
		seedSpec  = flag.String("seeds", "0-9", "inclusive seed range A-B")
		ticks     = flag.Int("ticks", 60*120, "ticks per match")
		out       = flag.String("out", "", "output shard path (required unless -dump-meta)")
		subIdle   = flag.Float64("subsample-idle", 1.0, "keep probability for fully-idle records")
		maxRec    = flag.Int("max-records", 0, "stop after this many records (0 = no cap)")
		dumpMeta  = flag.String("dump-meta", "", "write dataset_meta.json to this path and exit")
	)
	flag.Parse()

	if *dumpMeta != "" {
		writeMeta(*dumpMeta)
		return
	}
	if *out == "" {
		log.Fatal("datagen: -out is required")
	}

	geom, ok := config.PresetByName(*field)
	if !ok {
		log.Fatalf("datagen: unknown field preset %q", *field)
	}
	skill, ok := control.SkillFromString(*skillName)
	if !ok || (skill != control.SkillHard && skill != control.SkillImpossible) {
		log.Fatalf("datagen: -skill must be hard or impossible, got %q", *skillName)
	}
	neuralMode := *actorName == "neural"
	var actorNet *policy.Net
	if neuralMode {
		if *weights == "" {
			log.Fatal("datagen: -actor neural requires -weights")
		}
		wf, err := os.Open(*weights)
		if err != nil {
			log.Fatalf("datagen: open weights: %v", err)
		}
		actorNet, err = policy.Load(wf)
		wf.Close()
		if err != nil {
			log.Fatalf("datagen: load weights: %v", err)
		}
		if err := neural.ValidateNet(actorNet); err != nil {
			log.Fatalf("datagen: %v", err)
		}
	} else if *actorName != "teacher" {
		log.Fatalf("datagen: -actor must be teacher or neural, got %q", *actorName)
	}
	seedLo, seedHi := parseSeeds(*seedSpec)

	mutate := func(cfg *config.Config) {
		cfg.Geometry = geom
		cfg.Ruleset.OffsideEnabled = *offside
		if *offside && cfg.Ruleset.OffsideFrac == 0 {
			cfg.Ruleset.OffsideFrac = 0.5
		}
		if *boxcaps {
			cfg.Ruleset.PenaltyBoxMaxOpponents = 3
			cfg.Ruleset.GoalAreaKeeperOnly = true
		}
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("datagen: create %s: %v", *out, err)
	}
	defer f.Close()

	heads := neural.HeadSizes()
	featDim := neural.FlatDim
	stride := featDim*4 + len(heads)*4
	// Placeholder header (count patched at the end).
	if _, err := f.Write(make([]byte, 64)); err != nil {
		log.Fatal(err)
	}

	flags := fieldFlag(*field)
	if *offside {
		flags |= 1
	}
	if *boxcaps {
		flags |= 2
	}

	rng := rand.New(rand.NewSource(seedLo))
	obs := make([]float32, featDim)
	rec := make([]byte, stride)
	var count uint64

	for seed := seedLo; seed <= seedHi; seed++ {
		m := buildMatch(*size, seed, mutate)
		teachers := make(map[int]control.Controller, len(m.Players)) // label source
		feats := make(map[int]*neural.Controller, len(m.Players))    // observation featurizers
		actors := make(map[int]control.Controller, len(m.Players))   // who drives (DAgger only)
		for _, p := range m.Players {
			teachers[p.PlayerID] = control.NewAISkill(p.PlayerID, skill)
			feats[p.PlayerID] = neural.NewFeaturizer(p.PlayerID)
			if neuralMode {
				actors[p.PlayerID] = neural.New(p.PlayerID, actorNet)
			}
		}
		for tick := 0; tick < *ticks; tick++ {
			view := m.View()
			intents := make(map[int]sim.Intent, len(m.Players))
			for _, p := range m.Players { // slice order: deterministic
				id := p.PlayerID
				me, ok := view.Me(id)
				if !ok {
					continue
				}
				// The teacher labels the visited state (called once/tick). In BC mode it also
				// drives; in DAgger mode the neural clone drives the state distribution.
				teacherIntent := teachers[id].Intent(view)
				actorIntent := teacherIntent
				if neuralMode {
					actorIntent = actors[id].Intent(view)
				}
				intents[id] = actorIntent

				feats[id].FeaturizeFlat(view, me, obs)
				label := neural.Discretize(view, me, teacherIntent)
				if label[3] == neural.AbilNone && label[0] == neural.IdleMove && *subIdle < 1.0 {
					if rng.Float64() > *subIdle {
						continue
					}
				}
				packRecord(rec, obs, label[:])
				if _, err := f.Write(rec); err != nil {
					log.Fatal(err)
				}
				count++
				if *maxRec > 0 && count >= uint64(*maxRec) {
					goto done
				}
			}
			m.Step(intents, dt)
		}
	}
done:
	writeHeader(f, count, featDim, heads, seedLo, *size, *size, flags)
	log.Printf("datagen: wrote %d records (%d bytes/rec) to %s [size=%d field=%s offside=%v boxcaps=%v skill=%s seeds=%d-%d]",
		count, stride, *out, *size, *field, *offside, *boxcaps, *skillName, seedLo, seedHi)
}

func packRecord(rec []byte, obs []float32, label []int) {
	for i, v := range obs {
		le.PutUint32(rec[i*4:], math.Float32bits(v))
	}
	base := len(obs) * 4
	for i, lab := range label {
		le.PutUint32(rec[base+i*4:], uint32(int32(lab)))
	}
}

func writeHeader(f *os.File, count uint64, featDim int, heads []int, seed int64, teamL, teamR int, flags uint32) {
	var h [64]byte
	copy(h[0:8], "PHBLDAT1")
	le.PutUint32(h[8:], 1)
	le.PutUint32(h[12:], uint32(featDim))
	h[16] = byte(len(heads))
	for i, s := range heads {
		if i < 8 {
			h[17+i] = byte(s)
		}
	}
	le.PutUint64(h[32:], count)
	le.PutUint32(h[40:], uint32(featDim*4+len(heads)*4))
	le.PutUint64(h[44:], uint64(seed))
	le.PutUint32(h[52:], uint32(teamL))
	le.PutUint32(h[56:], uint32(teamR))
	le.PutUint32(h[60:], flags)
	if _, err := f.WriteAt(h[:], 0); err != nil {
		log.Fatal(err)
	}
}

func writeMeta(path string) {
	b, err := json.MarshalIndent(neural.Meta(), "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("datagen: wrote %s", path)
}

func fieldFlag(name string) uint32 {
	switch name {
	case "small", "futsal":
		return 0 << 2
	case "large", "big":
		return 2 << 2
	default: // medium / standard
		return 1 << 2
	}
}

func parseSeeds(spec string) (int64, int64) {
	parts := strings.SplitN(spec, "-", 2)
	lo, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		log.Fatalf("datagen: bad -seeds %q: %v", spec, err)
	}
	hi := lo
	if len(parts) == 2 {
		hi, err = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			log.Fatalf("datagen: bad -seeds %q: %v", spec, err)
		}
	}
	if hi < lo {
		log.Fatalf("datagen: -seeds high (%d) < low (%d)", hi, lo)
	}
	return lo, hi
}
