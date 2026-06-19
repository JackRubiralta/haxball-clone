#!/usr/bin/env bash
# Final checkpoint selection: evaluate every candidate weights file (PPO snapshots + the BC floor
# + the current embedded best) over a ROBUST grid (many seeds, multiple sizes/fields), score each
# by a blended win-rate, and copy the true best into the embedded weights. This replaces the noisy
# in-training gate for the authoritative ship decision.
set -uo pipefail
cd "$(dirname "$0")/.."   # repo root
EVAL=/tmp/phball_eval
PY=training/.venv/bin/python
META=training/shards/dataset_meta.json
EMB=internal/policy/weights/neural_v1.bin
SEEDS=${SEEDS:-30}
OPP=${OPP:-normal,hard,impossible}
SIZES=${SIZES:-3,4,5}
FIELDS=${FIELDS:-medium,large}
TICKS=${TICKS:-3600}

go build -o "$EVAL" ./cmd/eval

# Export the BC floor and the current PPO best to candidate .bin files.
mkdir -p training/candidates
export PYTHONPATH=training
[ -f training/checkpoints/bc_full.pt ] && $PY -m phball.export --checkpoint training/checkpoints/bc_full.pt --meta "$META" --weights-out training/candidates/bc_full.bin --no-parity >/dev/null 2>&1
[ -f training/checkpoints/ppo.pt.best ] && $PY -m phball.export --checkpoint training/checkpoints/ppo.pt.best --meta "$META" --weights-out training/candidates/ppo_best.bin --no-parity >/dev/null 2>&1
[ -f training/checkpoints/ppo.pt ] && $PY -m phball.export --checkpoint training/checkpoints/ppo.pt --meta "$META" --weights-out training/candidates/ppo_latest.bin --no-parity >/dev/null 2>&1

best_score=-1; best_file=""
score_of(){ # reads eval JSON on stdin -> prints "score win_h win_i poss_h pass_h"
  $PY -c '
import json,sys
d=json.load(sys.stdin)["opponents"]
def g(o,k): return d.get(o,{}).get(k,0)
sc=0.5*g("hard","win_rate")+0.5*g("impossible","win_rate")+0.1*g("hard","possession_pct")+0.05*g("normal","win_rate")
print(f"{sc:.4f} {g(\"hard\",\"win_rate\"):.3f} {g(\"impossible\",\"win_rate\"):.3f} {g(\"hard\",\"possession_pct\"):.3f} {g(\"hard\",\"pass_completion\"):.3f}")'
}

cands=( training/snapshots/snap_*.bin training/candidates/*.bin )
echo "candidate                         score  winH  winI  possH passH"
for f in "${cands[@]}"; do
  [ -f "$f" ] || continue
  line=$("$EVAL" -weights "$f" -seeds "$SEEDS" -sizes "$SIZES" -fields "$FIELDS" -opponents "$OPP" -ticks "$TICKS" 2>/dev/null | score_of)
  sc=$(echo "$line" | awk '{print $1}')
  printf "%-32s  %s\n" "$(basename "$f")" "$line"
  if awk "BEGIN{exit !($sc>$best_score)}"; then best_score=$sc; best_file=$f; fi
done

echo "BEST: $best_file (score $best_score)"
if [ -n "$best_file" ]; then
  cp "$best_file" "$EMB"
  echo "shipped $best_file -> $EMB"
fi
