"""PPO + GAE self-play with a PFSP league, initialized from the BC checkpoint. The learner drives
one side of many cmd/env workers; opponents (rule AI or frozen snapshots) run in-process in Go.
Actions are factored discrete heads with invalid-action masking applied both at sampling time and
when recomputing log-probs/entropy. A fixed team size/field is used per run so agents stack into
one batch; periodically the current policy is exported to a frozen snapshot and added to the
league, and a checkpoint is saved for the Go eval gate.

This is the headline RL phase; BC+DAgger is the shippable fallback if it underdelivers."""

import argparse
import json
import os
import shutil
import subprocess
import sys
import time

import numpy as np
import torch
import torch.nn as nn

from .env_client import EnvClient
from .league import League
from .meta import load_meta
from .model import DeepSetsPolicy


def head_offsets(head_sizes):
    off = [0]
    for s in head_sizes:
        off.append(off[-1] + s)
    return off


class MaskedPolicy:
    """Wraps the model with masked factored-categorical sampling and evaluation."""

    def __init__(self, model, head_sizes, device):
        self.model = model
        self.head_sizes = head_sizes
        self.off = head_offsets(head_sizes)
        self.device = device

    def _dists(self, logits, mask):
        ds = []
        for h, lg in enumerate(logits):
            mh = mask[:, self.off[h]:self.off[h + 1]]
            lg = lg.masked_fill(~mh, -1e9)
            ds.append(torch.distributions.Categorical(logits=lg))
        return ds

    @torch.no_grad()
    def act(self, obs, mask):
        logits, value = self.model(obs)
        ds = self._dists(logits, mask)
        actions = torch.stack([d.sample() for d in ds], dim=1)  # [B, H]
        logp = sum(ds[h].log_prob(actions[:, h]) for h in range(len(ds)))
        return actions, logp, value

    def evaluate(self, obs, mask, actions):
        logits, value = self.model(obs)
        ds = self._dists(logits, mask)
        logp = sum(ds[h].log_prob(actions[:, h]) for h in range(len(ds)))
        ent = sum(d.entropy() for d in ds)
        return logp, ent, value


def export_snapshot(checkpoint_path, meta_path, weights_path):
    """Invoke the exporter to write a frozen snapshot the Go env can load as an opponent."""
    subprocess.run([sys.executable, "-m", "phball.export", "--checkpoint", checkpoint_path,
                    "--meta", meta_path, "--weights-out", weights_path, "--no-parity"], check=True)


def eval_weights(eval_bin, weights_path, seeds, ticks):
    """Run the Go eval gate on a small grid; return the parsed report (or None on failure)."""
    try:
        out = subprocess.run(
            [eval_bin, "--weights", weights_path, "--seeds", str(seeds), "--sizes", "6",
             "--fields", "large", "--opponents", "normal,hard,impossible", "--ticks", str(ticks)],
            capture_output=True, text=True, timeout=1200, check=True).stdout
        return json.loads(out)
    except Exception as e:
        print(f"ppo: eval failed: {e}", flush=True)
        return None


def gate_score(report):
    """A scalar quality score from the eval report: win rates vs hard/impossible weighted, with
    possession vs hard as a tie-breaker."""
    o = report["opponents"]
    return (0.5 * o.get("hard", {}).get("win_rate", 0)
            + 0.5 * o.get("impossible", {}).get("win_rate", 0)
            + 0.1 * o.get("hard", {}).get("possession_pct", 0)
            + 0.05 * o.get("normal", {}).get("win_rate", 0))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bc", required=True, help="BC checkpoint to initialize from")
    ap.add_argument("--meta", required=True)
    ap.add_argument("--env-bin", default="/tmp/phball_env")
    ap.add_argument("--out", default="training/checkpoints/ppo.pt")
    ap.add_argument("--snapshot-dir", default="training/snapshots")
    ap.add_argument("--size", type=int, default=3)
    ap.add_argument("--field", default="medium")
    ap.add_argument("--n-envs", type=int, default=12)
    ap.add_argument("--rollout", type=int, default=128, help="env steps per update")
    ap.add_argument("--episode-len", type=int, default=256, help="steps before resampling opponent/seed")
    ap.add_argument("--frame-skip", type=int, default=6)
    ap.add_argument("--updates", type=int, default=2000)
    ap.add_argument("--epochs", type=int, default=4)
    ap.add_argument("--minibatch", type=int, default=8192)
    ap.add_argument("--lr", type=float, default=3e-4)
    ap.add_argument("--gamma", type=float, default=0.995)
    ap.add_argument("--lam", type=float, default=0.95)
    ap.add_argument("--clip", type=float, default=0.2)
    ap.add_argument("--ent-coef", type=float, default=0.01)
    ap.add_argument("--vf-coef", type=float, default=0.5)
    ap.add_argument("--anchor-frac", type=float, default=0.5, help="fraction of episodes vs rule anchors")
    ap.add_argument("--snapshot-every", type=int, default=150)
    ap.add_argument("--save-every", type=int, default=25)
    ap.add_argument("--eval-every", type=int, default=150, help="updates between Go eval gates (0=off)")
    ap.add_argument("--eval-seeds", type=int, default=8)
    ap.add_argument("--eval-ticks", type=int, default=2400)
    ap.add_argument("--eval-bin", default="/tmp/phball_eval")
    ap.add_argument("--ship-to", default="internal/policy/weights/neural_v1.bin",
                    help="embedded weights file to overwrite when a new best passes the gate")
    ap.add_argument("--device", default="cuda" if torch.cuda.is_available() else "cpu")
    ap.add_argument("--seconds", type=int, default=0, help="wall-clock budget; 0 = use --updates")
    args = ap.parse_args()

    meta = load_meta(args.meta)
    head_sizes = meta["head_sizes"]
    total_logits = sum(head_sizes)
    flat = meta["feature_dim"]
    device = torch.device(args.device)

    ckpt = torch.load(args.bc, map_location=device, weights_only=False)
    arch = ckpt.get("arch", {"phi_hidden": 64, "phi_out": 64, "trunk_hidden": 256})
    model = DeepSetsPolicy(meta, **arch).to(device)
    model.load_state_dict(ckpt["state_dict"])
    pol = MaskedPolicy(model, head_sizes, device)
    opt = torch.optim.Adam(model.parameters(), lr=args.lr)
    os.makedirs(os.path.dirname(args.out), exist_ok=True)

    import glob
    league = League(args.snapshot_dir, anchor_frac=args.anchor_frac)
    for snap in sorted(glob.glob(os.path.join(args.snapshot_dir, "*.bin"))):
        league.add_snapshot(snap)  # restart-safe: reuse snapshots from a prior run
    envs = [EnvClient(args.env_bin, flat, total_logits) for _ in range(args.n_envs)]
    size = args.size
    # Per-env opponent index + step counter, so we resample opponents/seeds periodically.
    opp_idx = [0] * args.n_envs
    step_in_ep = [0] * args.n_envs
    ep_reward = [0.0] * args.n_envs  # accumulated team reward this episode (~goal diff; sparse dominates)
    seed_ctr = 100000

    def reset_env(i):
        nonlocal seed_ctr
        oi = league.sample()
        opp_idx[i] = oi
        kind, spec = league.spec(oi)
        seed_ctr += 1
        if kind == "rule":
            cur = envs[i].reset(size, args.field, False, args.frame_skip, seed_ctr, 0, spec)
        else:
            cur = envs[i].reset(size, args.field, False, args.frame_skip, seed_ctr, 0, "frozen", spec)
        step_in_ep[i] = 0
        return cur

    cur = [reset_env(i) for i in range(args.n_envs)]
    B = args.n_envs * size  # agents per batch (assumes every env has `size` controlled agents)

    t_start = time.time()
    upd = 0
    best_gate = -1.0
    tmp_w = args.out + ".eval.bin"
    while True:
        if args.seconds and time.time() - t_start > args.seconds:
            break
        if not args.seconds and upd >= args.updates:
            break
        upd += 1

        obs_buf = torch.zeros(args.rollout, B, flat, device=device)
        mask_buf = torch.zeros(args.rollout, B, total_logits, dtype=torch.bool, device=device)
        act_buf = torch.zeros(args.rollout, B, len(head_sizes), dtype=torch.long, device=device)
        logp_buf = torch.zeros(args.rollout, B, device=device)
        val_buf = torch.zeros(args.rollout, B, device=device)
        rew_buf = torch.zeros(args.rollout, B, device=device)

        for t in range(args.rollout):
            obs = np.concatenate([c["obs"] for c in cur], axis=0)
            mask = np.concatenate([c["mask"] for c in cur], axis=0)
            obs_t = torch.from_numpy(obs).to(device)
            mask_t = torch.from_numpy(mask).to(device)
            actions, logp, value = pol.act(obs_t, mask_t)
            obs_buf[t] = obs_t
            mask_buf[t] = mask_t
            act_buf[t] = actions
            logp_buf[t] = logp
            val_buf[t] = value
            acts = actions.cpu().numpy().reshape(args.n_envs, size, len(head_sizes))
            rewards = np.zeros((args.n_envs, size), dtype=np.float32)
            # Pipeline the IPC: send STEP to every env, then collect, so the Go workers compute in
            # parallel rather than one round-trip at a time.
            for i in range(args.n_envs):
                envs[i].step_send(acts[i])
            for i in range(args.n_envs):
                nxt = envs[i].step_recv()
                rewards[i] = nxt["reward"]
                cur[i] = nxt
                step_in_ep[i] += 1
                ep_reward[i] += float(nxt["reward"].mean())
                if step_in_ep[i] >= args.episode_len:
                    # Classify the episode from accumulated team reward (sparse goal terms dominate;
                    # dense is capped well below 1), so PFSP weights opponents by real win rate.
                    r = ep_reward[i]
                    result = 1.0 if r > 0.5 else (0.0 if r < -0.5 else 0.5)
                    league.record(opp_idx[i], result)
                    ep_reward[i] = 0.0
                    cur[i] = reset_env(i)
            rew_buf[t] = torch.from_numpy(rewards.reshape(-1)).to(device)

        # Bootstrap value.
        with torch.no_grad():
            obs = np.concatenate([c["obs"] for c in cur], axis=0)
            _, last_val = model(torch.from_numpy(obs).to(device))

        # GAE (treat as non-terminal/truncated: bootstrap, no done masking for friendly matches).
        adv = torch.zeros(args.rollout, B, device=device)
        last_gae = torch.zeros(B, device=device)
        for t in reversed(range(args.rollout)):
            next_val = last_val if t == args.rollout - 1 else val_buf[t + 1]
            delta = rew_buf[t] + args.gamma * next_val - val_buf[t]
            last_gae = delta + args.gamma * args.lam * last_gae
            adv[t] = last_gae
        ret = adv + val_buf

        b_obs = obs_buf.reshape(-1, flat)
        b_mask = mask_buf.reshape(-1, total_logits)
        b_act = act_buf.reshape(-1, len(head_sizes))
        b_logp = logp_buf.reshape(-1)
        b_adv = adv.reshape(-1)
        b_ret = ret.reshape(-1)
        b_adv = (b_adv - b_adv.mean()) / (b_adv.std() + 1e-8)

        n = b_obs.shape[0]
        idx = np.arange(n)
        pi_loss = v_loss = ent_val = 0.0
        for _ in range(args.epochs):
            np.random.shuffle(idx)
            for s in range(0, n, args.minibatch):
                mb = idx[s:s + args.minibatch]
                mbt = torch.from_numpy(mb).to(device)
                logp, ent, value = pol.evaluate(b_obs[mbt], b_mask[mbt], b_act[mbt])
                ratio = torch.exp(logp - b_logp[mbt])
                a = b_adv[mbt]
                s1 = ratio * a
                s2 = torch.clamp(ratio, 1 - args.clip, 1 + args.clip) * a
                pl = -torch.min(s1, s2).mean()
                vl = 0.5 * (value - b_ret[mbt]).pow(2).mean()
                el = ent.mean()
                loss = pl + args.vf_coef * vl - args.ent_coef * el
                opt.zero_grad(set_to_none=True)
                loss.backward()
                nn.utils.clip_grad_norm_(model.parameters(), 0.5)
                opt.step()
                pi_loss, v_loss, ent_val = pl.item(), vl.item(), el.item()

        if upd % 5 == 0:
            print(f"ppo: upd {upd} rew/step={rew_buf.mean().item():.4f} pi={pi_loss:.3f} "
                  f"v={v_loss:.3f} ent={ent_val:.3f} t={time.time() - t_start:.0f}s", flush=True)
        if upd % args.save_every == 0:
            torch.save({"state_dict": model.state_dict(), "meta": meta, "arch": arch, "update": upd}, args.out)
        if upd % args.snapshot_every == 0:
            torch.save({"state_dict": model.state_dict(), "meta": meta, "arch": arch}, args.out)
            snap = os.path.join(args.snapshot_dir, f"snap_{upd:05d}.bin")
            try:
                export_snapshot(args.out, args.meta, snap)
                league.add_snapshot(snap)
                print(f"ppo: added snapshot {snap}; league: {league.summary()}", flush=True)
            except Exception as e:
                print(f"ppo: snapshot export failed: {e}", flush=True)

        if args.eval_every and upd % args.eval_every == 0:
            torch.save({"state_dict": model.state_dict(), "meta": meta, "arch": arch, "update": upd}, args.out)
            try:
                export_snapshot(args.out, args.meta, tmp_w)
                rep = eval_weights(args.eval_bin, tmp_w, args.eval_seeds, args.eval_ticks)
                if rep is not None:
                    sc = gate_score(rep)
                    o = rep["opponents"]
                    print(f"ppo: GATE upd {upd} score={sc:.3f} "
                          f"win[norm/hard/imp]={o['normal']['win_rate']:.2f}/{o['hard']['win_rate']:.2f}/"
                          f"{o['impossible']['win_rate']:.2f} poss_hard={o['hard']['possession_pct']:.2f} "
                          f"pass_hard={o['hard']['pass_completion']:.2f}", flush=True)
                    if sc > best_gate:
                        best_gate = sc
                        shutil.copy(tmp_w, args.ship_to)
                        torch.save({"state_dict": model.state_dict(), "meta": meta, "arch": arch,
                                    "update": upd, "gate": sc}, args.out + ".best")
                        print(f"ppo: SHIPPED new best (score {sc:.3f}) -> {args.ship_to}", flush=True)
            except Exception as e:
                print(f"ppo: eval/ship failed: {e}", flush=True)

    torch.save({"state_dict": model.state_dict(), "meta": meta, "arch": arch, "update": upd}, args.out)
    for e in envs:
        e.close()
    print(f"ppo: done at update {upd}; checkpoint {args.out}")


if __name__ == "__main__":
    main()
