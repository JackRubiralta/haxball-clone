#!/usr/bin/env bash
# Text status of the running tiki-taka training: current stage + the latest telemetry panel.
# Usage: bash training/status.sh        (one-shot)
#        watch -n5 bash training/status.sh   (live, refreshing)
LOG="${1:-training/tikitaka.log}"
[ -f "$LOG" ] || { echo "no training log at $LOG"; exit 1; }
echo "=== current stage ==="
grep -E "STAGE [0-9]" "$LOG" | tail -1
grep -E "GATE met" "$LOG" | tail -1
echo "=== latest eval panel (how it's playing the current drill) ==="
grep -E "score=.*poss=" "$LOG" | tail -1
echo "=== latest best shipped ==="
grep "NEW BEST" "$LOG" | tail -1
echo "=== latest training step ==="
grep -E "s[0-9]+ upd" "$LOG" | tail -1
pgrep -x python >/dev/null 2>&1 && echo "[training: RUNNING]" || echo "[training: STOPPED]"
