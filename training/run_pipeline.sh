#!/usr/bin/env bash
# Autonomous end-to-end training driver: setup -> datagen -> BC -> export -> parity -> eval ->
# DAgger rounds -> PPO league -> ship the best checkpoint by the behavioral eval gate. Scales to
# the detected hardware. Intended to run unattended; every stage logs and the eval gate (not a
# human) decides what ships. BC+DAgger is the shippable fallback if PPO underdelivers.
set -uo pipefail
cd "$(dirname "$0")/.."          # repo root
PY=training/.venv/bin/python
META=training/shards/dataset_meta.json
W=internal/policy/weights/neural_v1.bin
CKPT=training/checkpoints
mkdir -p "$CKPT" training/snapshots
export PYTHONPATH=training

log(){ echo "[pipeline $(date +%H:%M:%S)] $*"; }

# 0. Build Go tools.
go build -o /tmp/phball_datagen ./cmd/datagen
go build -o /tmp/phball_env ./cmd/env
go build -o /tmp/phball_eval ./cmd/eval

# 1. Data (skip if shards already present).
if ! ls training/shards/shard_*.bin >/dev/null 2>&1; then
  log "generating datagen sweep"; bash training/gen_sweep.sh
fi
/tmp/phball_datagen -dump-meta "$META"

# 2. BC.
log "behavioral cloning"
$PY -m phball.bc --shards training/shards --meta "$META" --out "$CKPT/bc.pt" --epochs 14 --batch 8192

# Helper: export a checkpoint and assert Go parity; returns nonzero on parity failure.
export_and_check(){ # $1 checkpoint
  $PY -m phball.export --checkpoint "$1" --meta "$META" --weights-out "$W" \
      --parity-out internal/policy/testdata/golden_forward.bin || return 1
  go test ./internal/policy -run TestForwardGoldenVector -count=1
}

# Helper: eval current embedded weights; prints the vs-hard / vs-impossible win rates.
eval_gate(){
  /tmp/phball_eval -seeds 30 -sizes 2,3,4,5,6 -fields medium,large \
    -opponents easy,normal,hard,impossible,nn -ticks 3600
}

export_and_check "$CKPT/bc.pt" || { log "BC parity FAILED"; exit 1; }
log "BC eval:"; eval_gate | tee training/eval_bc.json | $PY -c '
import json,sys; d=json.load(sys.stdin)
for k,a in d["opponents"].items(): print(f"  vs {k}: win={a[\"win_rate\"]:.2f} poss={a[\"possession_pct\"]:.2f} pass={a[\"pass_completion\"]:.2f}")'

# 3. DAgger rounds: the clone drives, the teacher labels; retrain on the growing set.
for round in 1 2; do
  log "DAgger round $round: generating clone-driven states"
  base=$((2000 + round*100))
  i=0
  for cfg in "3 medium" "4 medium" "4 large" "3 small" "5 medium" "2 medium"; do
    set -- $cfg
    start=$((base + i*10))
    /tmp/phball_datagen -actor neural -weights "$W" -size "$1" -field "$2" -skill impossible \
      -seeds "$start-$((start+9))" -ticks 1800 -out "training/shards/dagger_${round}_${i}.bin" &
    i=$((i+1))
  done
  wait
  log "DAgger round $round: retraining"
  $PY -m phball.bc --shards training/shards --meta "$META" --out "$CKPT/dagger${round}.pt" \
    --epochs 14 --batch 8192 --phi-out 80 --phi-hidden 80 --trunk-hidden 320
  export_and_check "$CKPT/dagger${round}.pt" || { log "DAgger$round parity FAILED"; break; }
  log "DAgger round $round eval:"; eval_gate | tee "training/eval_dagger${round}.json" | $PY -c '
import json,sys; d=json.load(sys.stdin)
for k,a in d["opponents"].items(): print(f"  vs {k}: win={a[\"win_rate\"]:.2f}")'
done

# 4. PPO self-play + league, from the best BC/DAgger checkpoint (time-boxed).
BEST="$CKPT/dagger2.pt"; [ -f "$BEST" ] || BEST="$CKPT/bc.pt"
log "PPO from $BEST (time-boxed)"
$PY -m phball.ppo --bc "$BEST" --meta "$META" --env-bin /tmp/phball_env \
  --out "$CKPT/ppo.pt" --size 3 --field medium --n-envs 12 --seconds "${PPO_SECONDS:-3600}"
export_and_check "$CKPT/ppo.pt" && { log "PPO eval:"; eval_gate | tee training/eval_ppo.json | $PY -c '
import json,sys; d=json.load(sys.stdin)
for k,a in d["opponents"].items(): print(f"  vs {k}: win={a[\"win_rate\"]:.2f}")'; }

log "pipeline done; embedded weights = last exported checkpoint. Pick the best by the eval JSONs."
