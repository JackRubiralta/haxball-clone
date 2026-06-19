# Neural-AI training (`neural` difficulty tier)

Trains the neural controller that ships as the `neural` tier. The Go side owns featurization,
inference, and evaluation; Python only trains and exports `float32` weights. Pipeline:

```
datagen (Go) ──► shards ──► BC (PyTorch) ──► export (PyTorch) ──► neural_v1.bin + parity vectors
                              ▲                                         │
                              │ DAgger: clone acts, teacher labels      ├─► Go parity test (bit-exact)
                              └──────── datagen -actor neural ◄─────────┤
                                                                        └─► cmd/eval (Go) behavioral gate
PPO self-play + PFSP league (PyTorch + cmd/env workers) ──► snapshots ──► export ──► eval ──► ship best
```

## Contract (zero Go/Python skew)

Go is the single source of truth for features and the action factorization. `cmd/datagen
-dump-meta` writes `shards/dataset_meta.json`; the Python model is built from it. Each shard is
self-describing (64-byte header) and validated against the meta on load. The exporter writes the
`PHNNW1` weight file **and** parity vectors computed with a sequential `float32` reduction that
mirrors `internal/policy` exactly, so `go test ./internal/policy -run TestForwardGoldenVector`
proves Go inference == the exported net **bit-for-bit**. Never change the feature layout in only
one place.

## Setup

```
python3 -m venv training/.venv
training/.venv/bin/pip install -r training/requirements.txt   # torch>=2.12 (cp314 + CUDA 13), numpy
```

## Stages (run from the repo root)

```bash
# 1. Data (parallel sweep across sizes/fields/skills; seeds >=1000, disjoint from the eval grid)
bash training/gen_sweep.sh
go run ./cmd/datagen -dump-meta training/shards/dataset_meta.json

# 2. Behavioral Cloning  (PYTHONPATH=training so `phball` imports)
training/.venv/bin/python -m phball.bc --shards training/shards \
  --meta training/shards/dataset_meta.json --out training/checkpoints/bc.pt --epochs 14

# 3. Export + parity gate
PYTHONPATH=training training/.venv/bin/python -m phball.export \
  --checkpoint training/checkpoints/bc.pt --meta training/shards/dataset_meta.json
go test ./internal/policy -run TestForwardGoldenVector

# 4. Behavioral eval (aggregate over a seed x size x field grid; NN side alternated)
go run ./cmd/eval -seeds 30 -sizes 2,3,4,5,6 -fields medium,large -opponents easy,normal,hard,impossible,nn

# 5. DAgger round (clone acts, teacher labels the visited states), then re-run 2-4
go build -o /tmp/phball_datagen ./cmd/datagen
/tmp/phball_datagen -actor neural -weights internal/policy/weights/neural_v1.bin \
  -size 4 -field medium -seeds 2010-2019 -out training/shards/dagger_x.bin

# 6. PPO self-play + PFSP league, initialized from the best BC/DAgger checkpoint
go build -o /tmp/phball_env ./cmd/env
PYTHONPATH=training training/.venv/bin/python -m phball.ppo \
  --bc training/checkpoints/bc.pt --meta training/shards/dataset_meta.json --env-bin /tmp/phball_env
```

`run_pipeline.sh` chains all of this autonomously (hardware-scaled, with the eval gate deciding
which checkpoint ships). The shipped weights live in `internal/policy/weights/neural_v1.bin`
(embedded); the `neural` tier loads them via `internal/aifactory`.
