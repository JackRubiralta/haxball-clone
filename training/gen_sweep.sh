#!/usr/bin/env bash
# Parallel datagen coverage sweep. Generates many disjoint-seed shards across team sizes, field
# presets, and teacher skills, parallelized across cores. Seeds are >= 1000 (disjoint from the
# cmd/eval grid's 0..29), spaced so ranges never overlap and ~1/10 of shards fall in the val
# bucket (start-seed bucket 0; see dataset.py _split_of).
set -euo pipefail
cd "$(dirname "$0")/.."   # repo root

BIN=/tmp/phball_datagen
OUT=training/shards
TICKS=${TICKS:-1800}
PAR=${PAR:-12}
REPEATS=${REPEATS:-2}     # passes over the config list (distinct seed ranges each)
mkdir -p "$OUT"
go build -o "$BIN" ./cmd/datagen
"$BIN" -dump-meta "$OUT/dataset_meta.json"

# (size field skill) configs.
configs=(
  "2 medium impossible" "3 medium impossible" "4 medium impossible" "5 medium impossible"
  "3 small impossible"  "4 small impossible"  "3 large impossible"  "4 large impossible"
  "2 small impossible"  "6 large impossible"  "3 medium hard"       "4 large hard"
)

jobs="$OUT/.jobs"
: > "$jobs"
i=0
for ((r=0; r<REPEATS; r++)); do
  for cfg in "${configs[@]}"; do
    start=$((1000 + i*10))
    seeds="${start}-$((start+9))"
    printf '%s %s %s\n' "$cfg" "$seeds" "$(printf '%s/shard_%03d.bin' "$OUT" "$i")" >> "$jobs"
    i=$((i+1))
  done
done
echo "sweep: $i shards, parallelism $PAR, ticks $TICKS"

# Each line: size field skill seeds out
xargs -P "$PAR" -L1 bash -c '
  "$0" -size "$1" -field "$2" -skill "$3" -seeds "$4" -ticks "'"$TICKS"'" -out "$5" >/dev/null 2>&1 \
    && echo "ok  $5" || echo "FAIL $5"
' "$BIN" < "$jobs"

total=$(du -sh "$OUT" | cut -f1)
echo "sweep: done; shard dir size $total"
ls "$OUT"/shard_*.bin | wc -l | xargs echo "sweep: shard count"
