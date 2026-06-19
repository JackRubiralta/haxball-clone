"""Behavioral Cloning: fit the DeepSetsPolicy to the rule-AI teacher's discretized actions.
Multi-head cross-entropy, with the ability (and cancel) head class-weighted by inverse frequency
so 'none' does not swamp shoot/trap/push. Train/val split is by shard (see dataset.py). Behavioral
quality is validated separately by exporting and running cmd/eval in Go; here we track per-head
val accuracy as a sanity signal and checkpoint the best.

With --preload the whole split is loaded into RAM tensors and trained with a manual minibatch
loop, so epochs are GPU-bound rather than memmap-random-access-bound (much faster for big corpora;
the corpus must fit in RAM)."""

import argparse
import json
import os
import time

import numpy as np
import torch
import torch.nn as nn
from torch.utils.data import DataLoader

from .dataset import ShardDataset, load_split_arrays
from .meta import load_meta
from .model import DeepSetsPolicy


def class_weights(counts, device):
    counts = np.maximum(counts, 1)
    k = len(counts)
    w = counts.sum() / (k * counts)
    w = np.clip(w, 0.2, 8.0).astype(np.float32)
    return torch.tensor(w, device=device)


def evaluate_loader(model, loader, device, head_sizes):
    model.eval()
    correct = np.zeros(len(head_sizes), dtype=np.int64)
    total = 0
    with torch.no_grad():
        for obs, lab in loader:
            obs = obs.to(device, non_blocking=True)
            lab = lab.to(device, non_blocking=True)
            logits, _ = model(obs)
            for h in range(len(head_sizes)):
                correct[h] += (logits[h].argmax(1) == lab[:, h]).sum().item()
            total += obs.shape[0]
    return (correct / max(total, 1)).tolist(), total


def evaluate_tensors(model, obs, lab, batch, device, head_sizes):
    model.eval()
    correct = np.zeros(len(head_sizes), dtype=np.int64)
    total = 0
    with torch.no_grad():
        for s in range(0, obs.shape[0], batch):
            ob = obs[s:s + batch].to(device, non_blocking=True)
            la = lab[s:s + batch].to(device, non_blocking=True)
            logits, _ = model(ob)
            for h in range(len(head_sizes)):
                correct[h] += (logits[h].argmax(1) == la[:, h]).sum().item()
            total += ob.shape[0]
    return (correct / max(total, 1)).tolist(), total


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--shards", required=True)
    ap.add_argument("--meta", required=True)
    ap.add_argument("--out", default="training/checkpoints/bc.pt")
    ap.add_argument("--epochs", type=int, default=8)
    ap.add_argument("--batch", type=int, default=4096)
    ap.add_argument("--lr", type=float, default=1e-3)
    ap.add_argument("--workers", type=int, default=4)
    ap.add_argument("--phi-out", type=int, default=64)
    ap.add_argument("--phi-hidden", type=int, default=64)
    ap.add_argument("--trunk-hidden", type=int, default=256)
    ap.add_argument("--preload", action="store_true", help="load the split into RAM tensors (fast)")
    ap.add_argument("--device", default="cuda" if torch.cuda.is_available() else "cpu")
    ap.add_argument("--max-steps", type=int, default=0, help="0 = full epochs")
    args = ap.parse_args()

    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    meta = load_meta(args.meta)
    head_sizes = meta["head_sizes"]
    H = len(head_sizes)
    feat = meta["feature_dim"]
    device = torch.device(args.device)
    print(f"bc: device={device} torch={torch.__version__} preload={args.preload}", flush=True)

    train_loader = val_loader = None
    obs_tr = lab_tr = obs_va = lab_va = None
    if args.preload:
        t0 = time.time()
        obs_tr_np, lab_tr_np = load_split_arrays(args.shards, "train", feat, H)
        obs_va_np, lab_va_np = load_split_arrays(args.shards, "val", feat, H)
        obs_tr = torch.from_numpy(obs_tr_np)
        lab_tr = torch.from_numpy(lab_tr_np)
        obs_va = torch.from_numpy(obs_va_np)
        lab_va = torch.from_numpy(lab_va_np)
        n_train, n_val = obs_tr.shape[0], obs_va.shape[0]
        abil_counts = np.bincount(lab_tr_np[:, 3], minlength=head_sizes[3])
        cancel_counts = np.bincount(lab_tr_np[:, 4], minlength=head_sizes[4])
        print(f"bc: preloaded train={n_train} val={n_val} in {time.time() - t0:.1f}s "
              f"({obs_tr.element_size() * obs_tr.nelement() / 1e9:.1f} GB obs)", flush=True)
    else:
        train = ShardDataset(args.shards, "train", feat, H)
        val = ShardDataset(args.shards, "val", feat, H)
        n_train, n_val = len(train), len(val)
        pin = device.type == "cuda"
        train_loader = DataLoader(train, batch_size=args.batch, shuffle=True, num_workers=args.workers,
                                  pin_memory=pin, drop_last=True, persistent_workers=args.workers > 0)
        val_loader = DataLoader(val, batch_size=args.batch, shuffle=False, num_workers=args.workers,
                                pin_memory=pin) if n_val else None
        abil_counts = train.ability_histogram(3, head_sizes[3])
        cancel_counts = train.ability_histogram(4, head_sizes[4])
    print(f"bc: train records={n_train} val records={n_val}", flush=True)
    if n_train == 0:
        raise SystemExit("bc: no training records found")

    arch = dict(phi_hidden=args.phi_hidden, phi_out=args.phi_out, trunk_hidden=args.trunk_hidden)
    model = DeepSetsPolicy(meta, **arch).to(device)
    print(f"bc: model params={sum(p.numel() for p in model.parameters())}", flush=True)

    abil_w = class_weights(abil_counts, device)
    cancel_w = class_weights(cancel_counts, device)
    print(f"bc: ability weights={abil_w.tolist()} cancel weights={cancel_w.tolist()}", flush=True)
    ce = [nn.CrossEntropyLoss() for _ in head_sizes]
    ce[3] = nn.CrossEntropyLoss(weight=abil_w)
    ce[4] = nn.CrossEntropyLoss(weight=cancel_w)

    opt = torch.optim.Adam(model.parameters(), lr=args.lr)

    def train_step(obs, lab):
        logits, _ = model(obs)
        loss = sum(ce[h](logits[h], lab[:, h]) for h in range(H))
        opt.zero_grad(set_to_none=True)
        loss.backward()
        opt.step()
        return loss.item()

    metrics = []
    best_score = -1.0
    for epoch in range(args.epochs):
        model.train()
        t0 = time.time()
        running = 0.0
        steps = 0
        if args.preload:
            perm = torch.randperm(n_train)
            for s in range(0, n_train - args.batch + 1, args.batch):
                sel = perm[s:s + args.batch]
                obs = obs_tr.index_select(0, sel).to(device, non_blocking=True)
                lab = lab_tr.index_select(0, sel).to(device, non_blocking=True)
                running += train_step(obs, lab)
                steps += 1
                if args.max_steps and steps >= args.max_steps:
                    break
        else:
            for obs, lab in train_loader:
                running += train_step(obs.to(device, non_blocking=True), lab.to(device, non_blocking=True))
                steps += 1
                if args.max_steps and steps >= args.max_steps:
                    break
        train_loss = running / max(steps, 1)
        if not np.isfinite(train_loss):
            raise SystemExit(f"bc: non-finite train loss at epoch {epoch}")

        acc, vtot = ([0.0] * H, 0)
        if args.preload and n_val:
            acc, vtot = evaluate_tensors(model, obs_va, lab_va, args.batch, device, head_sizes)
        elif val_loader is not None:
            acc, vtot = evaluate_loader(model, val_loader, device, head_sizes)
        score = 0.4 * acc[0] + 0.2 * acc[1] + 0.15 * acc[2] + 0.2 * acc[3] + 0.05 * acc[4]
        rec = dict(epoch=epoch, train_loss=round(train_loss, 4), val_acc=[round(a, 4) for a in acc],
                   val_records=vtot, score=round(score, 4), secs=round(time.time() - t0, 1))
        metrics.append(rec)
        print(f"bc: epoch {epoch}: loss={rec['train_loss']} val_acc={rec['val_acc']} "
              f"score={rec['score']} ({rec['secs']}s)", flush=True)
        if score >= best_score:
            best_score = score
            torch.save({"state_dict": model.state_dict(), "meta": meta, "arch": arch,
                        "val_acc": acc, "score": score, "epoch": epoch}, args.out)
        with open(args.out + ".metrics.json", "w") as f:
            json.dump(metrics, f, indent=2)

    print(f"bc: done; best score={best_score:.4f}; checkpoint={args.out}", flush=True)


if __name__ == "__main__":
    main()
