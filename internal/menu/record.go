package menu

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"phootball/internal/sim"
)

// statsDir is where finished-match JSON records are written (one file per game).
const statsDir = "phootball-matches"

// writeMatchRecord persists a finished match's stats + play-by-play as JSON. The filename is
// derived from the seed and final score (never the wall clock, so a replay of the same match
// produces the same name), under a dedicated subdirectory so it does not litter the cwd. All
// failures are logged at Warn and swallowed -- a stats write must never crash the game.
func writeMatchRecord(m *sim.Match) {
	if m == nil || m.Recorder() == nil {
		return
	}
	mr := m.Recorder().MatchRecord(m)
	if err := os.MkdirAll(statsDir, 0o755); err != nil {
		slog.Warn("stats: could not create output dir", "dir", statsDir, "err", err)
		return
	}
	name := "match-seed" + strconv.FormatInt(mr.Seed, 10) + "-" +
		strconv.Itoa(mr.FinalScore[0]) + "v" + strconv.Itoa(mr.FinalScore[1]) + ".json"
	path := filepath.Join(statsDir, name)
	f, err := os.Create(path)
	if err != nil {
		slog.Warn("stats: could not create record file", "path", path, "err", err)
		return
	}
	defer f.Close()
	if err := mr.WriteJSON(f); err != nil {
		slog.Warn("stats: could not write record", "path", path, "err", err)
		return
	}
	slog.Info("stats: wrote match record", "path", path, "events", len(mr.Events))
}
