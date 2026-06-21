#!/bin/bash
# tikitaka_watchdog.sh -- keep the tiki-taka training run alive for a fixed wall-clock window.
# If the learner (python) dies or the curriculum finishes early, it restarts, warm-resuming from the
# continuously-saved checkpoint at the last stage seen in the log, so a crash or a completed curriculum
# never leaves the GPU idle. Stops training cleanly when the window elapses. Detached: survives the
# agent's context. Logs to training/watchdog.log; the learner keeps appending to training/tikitaka.log
# so cmd/watch and the analyst keep following it.
set -u
cd /home/jack/Desktop/hax/haxball-clone || exit 1

HOURS="${1:-15}"
END=$(( $(date +%s) + HOURS * 3600 ))
LOG=training/watchdog.log
PY=training/.venv/bin/python
COMMON="--meta training/dataset_meta.json --env-bin /tmp/phball_env --lr 2.0e-4 \
  --override-p0 0.5 --bc-coef0 0.8 --bc-floor 0.15 --bc-anneal 700"

echo "[watchdog $(date)] start; will keep training alive for ${HOURS}h (until $(date -d @${END}))" >> "$LOG"

start_run() {
  # Resume from the rolling checkpoint at the last stage seen in the log (default scratch).
  local stage ckpt resume
  stage=$(grep -oE "STAGE [0-9]+" training/tikitaka.log 2>/dev/null | tail -1 | grep -oE "[0-9]+")
  stage=${stage:-0}
  if [ -f training/checkpoints/tikitaka.pt ]; then
    resume="--resume training/checkpoints/tikitaka.pt"
  else
    resume=""
  fi
  echo "[watchdog $(date)] (re)starting training at stage ${stage} ${resume:+(warm)}" >> "$LOG"
  # shellcheck disable=SC2086
  PYTHONPATH=training nohup "$PY" -m phball.tikitaka --start-stage "$stage" $resume $COMMON \
    >> training/tikitaka.log 2>&1 &
  sleep 90  # give it time to boot before the next liveness check
}

# Rebuild the env binary once up front in case it was cleared from /tmp.
[ -x /tmp/phball_env ] || go build -o /tmp/phball_env ./cmd/env 2>>"$LOG"

while [ "$(date +%s)" -lt "$END" ]; do
  if ! pgrep -x python >/dev/null 2>&1; then
    [ -x /tmp/phball_env ] || go build -o /tmp/phball_env ./cmd/env 2>>"$LOG"
    start_run
  fi
  sleep 120
done

echo "[watchdog $(date)] ${HOURS}h elapsed -- stopping training." >> "$LOG"
pkill -9 -x python 2>/dev/null
pkill -9 -x phball_env 2>/dev/null
echo "[watchdog $(date)] done." >> "$LOG"
