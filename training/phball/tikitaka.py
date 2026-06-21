"""From-scratch tiki-taka trainer: a curriculum of drill scenarios + self-play, PPO, NO teacher /
BC. Reuses the PPO machinery (MaskedPolicy, GAE, clipped update) from ppo.py, the Deep-Sets model,
the extended env IPC (scenarios + telemetry), and the curriculum/stage gates. It ships the best
policy (by a tiki-taka telemetry score) to a live-playable checkpoint throughout, and refreshes the
embedded weights + parity at each stage end so `-difficulty neural` tracks progress too.

Run fully autonomously; on a bad update (NaN) it skips and continues; stages advance on their gate
or budget. See TIKITAKA_AI_PROMPT.md."""

import argparse
import glob
import json
import os
import shutil
import subprocess
import sys
import time

import numpy as np
import torch
import torch.nn as nn

from .curriculum import STAGES, metrics_from_telemetry, tikitaka_score
from .env_client import EnvClient
from .model import DeepSetsPolicy
from .ppo import MaskedPolicy, export_snapshot
from .meta import load_meta


REPO_EMBED_WEIGHTS = "internal/policy/weights/neural_v1.bin"


def export_parity(checkpoint, meta_path, weights_out):
    """Export WITH parity vectors (regenerates the golden), so the embedded net + golden stay
    consistent and TestForwardGoldenVector passes. Only the real embedded ship regenerates the REPO
    golden testdata; any other (e.g. a /tmp smoke) writes its parity beside its weights, so a smoke
    can never desync the committed repo golden from the committed embedded net."""
    cmd = [sys.executable, "-m", "phball.export", "--checkpoint", checkpoint,
           "--meta", meta_path, "--weights-out", weights_out]
    if os.path.normpath(weights_out) != os.path.normpath(REPO_EMBED_WEIGHTS):
        cmd += ["--parity-out", weights_out + ".golden"]
    subprocess.run(cmd, check=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--meta", required=True)
    ap.add_argument("--env-bin", default="/tmp/phball_env")
    ap.add_argument("--out", default="training/checkpoints/tikitaka.pt")
    ap.add_argument("--snapshot-dir", default="training/snapshots")
    ap.add_argument("--ship-to", default="internal/policy/weights/neural_v1.bin")
    ap.add_argument("--latest", default="training/checkpoints/latest_best.bin")
    ap.add_argument("--resume", default="", help="checkpoint to resume the model weights from")
    ap.add_argument("--start-stage", type=int, default=0)
    ap.add_argument("--n-envs", type=int, default=14)
    ap.add_argument("--rollout", type=int, default=96)
    ap.add_argument("--frame-skip", type=int, default=6)
    ap.add_argument("--epochs", type=int, default=4)
    ap.add_argument("--minibatch", type=int, default=8192)
    ap.add_argument("--lr", type=float, default=2.5e-4)
    ap.add_argument("--gamma", type=float, default=0.995)
    ap.add_argument("--lam", type=float, default=0.95)
    ap.add_argument("--clip", type=float, default=0.2)
    ap.add_argument("--vf-coef", type=float, default=0.5)
    ap.add_argument("--phi-hidden", type=int, default=96)
    ap.add_argument("--phi-out", type=int, default=96)
    ap.add_argument("--trunk-hidden", type=int, default=384)
    ap.add_argument("--eval-every", type=int, default=120)
    ap.add_argument("--eval-episodes", type=int, default=2, help="eval episodes per env")
    ap.add_argument("--snapshot-every", type=int, default=200)
    ap.add_argument("--save-every", type=int, default=50)
    ap.add_argument("--override-p0", type=float, default=0.75,
                    help="initial teacher action-override probability for guided bootstrapping (annealed to 0)")
    ap.add_argument("--override-anneal", type=int, default=500,
                    help="updates over which the teacher-override probability anneals to 0 (per stage)")
    ap.add_argument("--bc-coef0", type=float, default=1.0,
                    help="initial weight of the BC/kickstart loss (supervises the policy toward teacher advice)")
    ap.add_argument("--bc-floor", type=float, default=0.15,
                    help="floor the BC weight holds at while a teacher stage runs (persistent imitation -> durable transfer)")
    ap.add_argument("--bc-anneal", type=int, default=700,
                    help="updates over which the BC weight decays from bc-coef0 to bc-floor (per stage)")
    ap.add_argument("--seconds", type=int, default=0, help="overall wall-clock budget (0 = run all stages to budget)")
    ap.add_argument("--device", default="cuda" if torch.cuda.is_available() else "cpu")
    args = ap.parse_args()

    meta = load_meta(args.meta)
    head_sizes = meta["head_sizes"]
    total_logits = sum(head_sizes)
    flat = meta["feature_dim"]
    nheads = len(head_sizes)
    device = torch.device(args.device)
    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    os.makedirs(args.snapshot_dir, exist_ok=True)

    arch = {"phi_hidden": args.phi_hidden, "phi_out": args.phi_out, "trunk_hidden": args.trunk_hidden}
    model = DeepSetsPolicy(meta, **arch).to(device)
    if args.resume:
        ck = torch.load(args.resume, map_location=device, weights_only=False)
        model.load_state_dict(ck["state_dict"])
        arch = ck.get("arch", arch)
        print(f"tikitaka: resumed from {args.resume}", flush=True)
    pol = MaskedPolicy(model, head_sizes, device)
    opt = torch.optim.Adam(model.parameters(), lr=args.lr)
    nparams = sum(p.numel() for p in model.parameters())
    print(f"tikitaka: device={device} params={nparams} flat={flat} heads={head_sizes}", flush=True)

    snapshots = sorted(glob.glob(os.path.join(args.snapshot_dir, "*.bin")))
    seed_ctr = [70000]
    t_start = time.time()
    best_score = -1.0

    def pick_opp(scn):
        """Resolve a scenario's opponent: a self-snapshot when self-play is active and one exists,
        else the scenario's scripted actor."""
        if scn["opp"] == "frozen" and snapshots:
            return "frozen", snapshots[np.random.randint(len(snapshots))]
        if scn["opp"] in ("easy", "normal", "hard", "impossible"):
            return scn["opp"], ""
        return "scripted", ""

    def reset_to(env, scn, p_override=0.0):
        seed_ctr[0] += 1
        opp, path = pick_opp(scn)
        return env.reset_scenario(scn["kind"], scn["home"], scn["away"], scn["field"], False,
                                  args.frame_skip, scn["profile"], 0, opp, seed_ctr[0],
                                  scn["episode_len"], frozen_path=path,
                                  teacher=scn.get("teacher", 0), p_override=p_override)

    def override_p(stage, upd):
        """Annealed teacher-override probability for guided bootstrapping (JSRL-style handoff): start
        high so the policy SEES the demonstrated behaviour (esp. passing), then fade to 0 so it learns
        to do it on its own and can surpass the teacher. Only stages with a teacher use it."""
        if not stage["scenarios"][0].get("teacher", 0):
            return 0.0
        anneal = max(stage["min_upd"], args.override_anneal)
        return args.override_p0 * max(0.0, 1.0 - upd / float(anneal))

    def bc_coef(stage, upd):
        """BC/kickstart weight: decays from bc_coef0 to a non-zero floor over the stage, so the teacher
        keeps supervising the policy toward the demonstrated action (esp. passing) -- the durable
        transfer the annealed action-override alone failed to produce. 0 for stages without a teacher."""
        if not stage["scenarios"][0].get("teacher", 0):
            return 0.0
        anneal = max(stage["min_upd"], args.bc_anneal)
        return max(args.bc_floor, args.bc_coef0 * max(0.0, 1.0 - upd / float(anneal)))

    envs = [EnvClient(args.env_bin, flat, total_logits) for _ in range(args.n_envs)]

    def run_eval(scn, episodes):
        """Reset every env to scn, play `episodes` episodes, return the aggregated telemetry list."""
        teles = []
        for _ in range(episodes):
            cur = [reset_to(e, scn) for e in envs]
            for _ in range(scn["episode_len"]):
                obs = np.concatenate([c["obs"] for c in cur], axis=0)
                mask = np.concatenate([c["mask"] for c in cur], axis=0)
                acts = pol.greedy(torch.from_numpy(obs).to(device),
                                  torch.from_numpy(mask).to(device)).cpu().numpy()
                off = 0
                for e, c in zip(envs, cur):
                    a = c["obs"].shape[0]
                    e.step_send(acts[off:off + a]); off += a
                for i, e in enumerate(envs):
                    cur[i] = e.step_recv()
            for e in envs:
                teles.append(e.telemetry())
        return teles

    def ship_if_best(tag, scn, no_score=False):
        nonlocal best_score
        teles = run_eval(scn, max(args.eval_episodes, 1))
        m = metrics_from_telemetry(teles)
        sc = tikitaka_score(m, no_score=no_score)
        print(f"tikitaka: [{tag}] score={sc:.3f} poss={m['possession_pct']:.2f} "
              f"pass/min={m['pass_per_min']:.1f} realpass={m['real_pass_frac']:.2f} "
              f"ownT/O={m['own_third_turnover_frac']:.2f} crowd={m['crowd_mean']:.2f} "
              f"gkbox={m['gk_box_mean']:.2f} push/min={m['push_per_min']:.1f} "
              f"shots/min={m['shots_per_min']:.1f} gd/ep={m['goal_diff_per_ep']:.2f}", flush=True)
        torch.save({"state_dict": model.state_dict(), "meta": meta, "arch": arch}, args.out)
        if sc > best_score:
            best_score = sc
            try:
                export_snapshot(args.out, args.meta, args.latest)  # fast, no parity -- live-playable
                torch.save({"state_dict": model.state_dict(), "meta": meta, "arch": arch, "score": sc},
                           args.out + ".best")
                print(f"tikitaka: NEW BEST score={sc:.3f} -> {args.latest}", flush=True)
            except Exception as ex:
                print(f"tikitaka: ship failed: {ex}", flush=True)
        return m

    # ---- curriculum ----
    for si in range(args.start_stage, len(STAGES)):
        stage = STAGES[si]
        ent_coef = stage["ent"]
        best_score = -1.0  # ship-best is per-stage (scores aren't comparable across scenarios)
        # Eval/gate/ship on THIS stage's own scenario, so the lesson is measured (not always 6v6).
        escn = dict(stage["scenarios"][0])
        # Assign each env a fixed scenario from the stage (so agent counts are constant -> one batch).
        env_scn = [stage["scenarios"][i % len(stage["scenarios"])] for i in range(args.n_envs)]
        cur = [reset_to(envs[i], env_scn[i], p_override=override_p(stage, 1)) for i in range(args.n_envs)]
        apx = [c["obs"].shape[0] for c in cur]   # agents per env (fixed this stage)
        offs = np.cumsum([0] + apx)
        B = int(offs[-1])
        step_in_ep = [0] * args.n_envs
        print(f"tikitaka: STAGE {si} '{stage['name']}' ent={ent_coef} B={B} apx={apx} "
              f"selfplay={stage['self_play']} snaps={len(snapshots)}", flush=True)

        upd = 0
        while True:
            if args.seconds and time.time() - t_start > args.seconds:
                break
            upd += 1
            obs_buf = torch.zeros(args.rollout, B, flat, device=device)
            mask_buf = torch.zeros(args.rollout, B, total_logits, dtype=torch.bool, device=device)
            act_buf = torch.zeros(args.rollout, B, nheads, dtype=torch.long, device=device)
            logp_buf = torch.zeros(args.rollout, B, device=device)
            val_buf = torch.zeros(args.rollout, B, device=device)
            rew_buf = torch.zeros(args.rollout, B, device=device)
            adv_buf = torch.zeros(args.rollout, B, nheads, dtype=torch.long, device=device)   # teacher advice (BC label)
            advok_buf = torch.zeros(args.rollout, B, dtype=torch.bool, device=device)          # advice valid this sample

            p_now = override_p(stage, upd)
            for t in range(args.rollout):
                obs = np.concatenate([c["obs"] for c in cur], axis=0)
                mask = np.concatenate([c["mask"] for c in cur], axis=0)
                obs_t = torch.from_numpy(obs).to(device)
                mask_t = torch.from_numpy(mask).to(device)
                actions, logp, value = pol.act(obs_t, mask_t)
                obs_buf[t], mask_buf[t], act_buf[t], logp_buf[t], val_buf[t] = obs_t, mask_t, actions, logp, value
                acts = actions.cpu().numpy()
                for i, e in enumerate(envs):
                    e.step_send(acts[offs[i]:offs[i + 1]])
                rewards = np.zeros(B, dtype=np.float32)
                exec_all = np.zeros((B, nheads), dtype=np.int64)
                adv_all = np.full((B, nheads), -1, dtype=np.int64)
                for i, e in enumerate(envs):
                    nxt = e.step_recv()
                    sl = slice(offs[i], offs[i + 1])
                    rewards[sl] = nxt["reward"]
                    exec_all[sl] = nxt["exec"]
                    adv_all[sl] = nxt["advice"]
                    cur[i] = nxt
                    step_in_ep[i] += 1
                    if step_in_ep[i] >= env_scn[i]["episode_len"]:
                        cur[i] = reset_to(e, env_scn[i], p_override=p_now)
                        step_in_ep[i] = 0
                rew_buf[t] = torch.from_numpy(rewards).to(device)
                # Guided bootstrapping: on teacher-demonstration episodes the env executes (and reports)
                # the teacher's action, not the policy's. Train PPO on the EXECUTED action: store it and
                # recompute its log-prob under the current policy (ratio ~1 at rollout, so it imitates).
                exec_t = torch.from_numpy(exec_all).to(device)
                if not torch.equal(exec_t, actions):
                    with torch.no_grad():
                        lp_exec, _, _ = pol.evaluate(obs_buf[t], mask_buf[t], exec_t)
                    act_buf[t] = exec_t
                    logp_buf[t] = lp_exec
                # Teacher advice for the BC/kickstart loss (valid where head 0 != -1).
                adv_t = torch.from_numpy(adv_all).to(device)
                advok_buf[t] = adv_t[:, 0] >= 0
                adv_buf[t] = adv_t.clamp_min(0)  # clamp the -1 sentinel so gather is safe; masked out by advok

            with torch.no_grad():
                obs = np.concatenate([c["obs"] for c in cur], axis=0)
                _, last_val = model(torch.from_numpy(obs).to(device))
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
            b_act = act_buf.reshape(-1, nheads)
            b_logp = logp_buf.reshape(-1)
            b_adv = adv.reshape(-1)
            b_ret = ret.reshape(-1)
            b_adv = (b_adv - b_adv.mean()) / (b_adv.std() + 1e-8)
            b_advidx = adv_buf.reshape(-1, nheads)
            b_advok = advok_buf.reshape(-1)
            bcc = bc_coef(stage, upd)

            n = b_obs.shape[0]
            idx = np.arange(n)
            pi_loss = v_loss = ent_val = bc_val = 0.0
            for _ in range(args.epochs):
                np.random.shuffle(idx)
                for s in range(0, n, args.minibatch):
                    mb = torch.from_numpy(idx[s:s + args.minibatch]).to(device)
                    logp, ent, value = pol.evaluate(b_obs[mb], b_mask[mb], b_act[mb])
                    ratio = torch.exp(logp - b_logp[mb])
                    a = b_adv[mb]
                    pl = -torch.min(ratio * a, torch.clamp(ratio, 1 - args.clip, 1 + args.clip) * a).mean()
                    vl = 0.5 * (value - b_ret[mb]).pow(2).mean()
                    el = ent.mean()
                    loss = pl + args.vf_coef * vl - ent_coef * el
                    # BC/kickstart: supervise the policy toward the teacher's recommended action at the
                    # states it actually visited (DAgger). This DIRECTLY raises logp(teacher action),
                    # unlike action-override whose PPO advantage ~0 on demo states transferred nothing.
                    bc_item = 0.0
                    if bcc > 0:
                        okmb = b_advok[mb]
                        if okmb.any():
                            lp_adv, _, _ = pol.evaluate(b_obs[mb], b_mask[mb], b_advidx[mb])
                            # Clamp per-sample log-prob: if the teacher ever advises a currently-masked
                            # action its logp is ~-inf, which spiked the BC loss to ~1e5. -20 bounds the
                            # supervised penalty per sample (a strong but finite push toward the advice).
                            lp_adv = lp_adv.clamp_min(-20.0)
                            bc = -(lp_adv * okmb.float()).sum() / okmb.float().sum().clamp_min(1.0)
                            loss = loss + bcc * bc
                            bc_item = bc.item()
                    if not torch.isfinite(loss):
                        print(f"tikitaka: non-finite loss at stage {si} upd {upd}; skipping batch", flush=True)
                        continue
                    opt.zero_grad(set_to_none=True)
                    loss.backward()
                    gn = nn.utils.clip_grad_norm_(model.parameters(), 0.5)
                    # Skip the step if the gradient is non-finite: clipping a NaN/inf gradient leaves it
                    # NaN, which would corrupt the weights and make every later forward NaN (a crash in
                    # Categorical). Dropping the bad minibatch keeps the run alive instead.
                    if not torch.isfinite(gn):
                        print(f"tikitaka: non-finite grad norm at stage {si} upd {upd}; skipping step", flush=True)
                        continue
                    opt.step()
                    pi_loss, v_loss, ent_val, bc_val = pl.item(), vl.item(), el.item(), bc_item

            if upd % 10 == 0:
                print(f"tikitaka: s{si} upd {upd} rew/step={rew_buf.mean().item():.4f} "
                      f"pi={pi_loss:.3f} v={v_loss:.3f} ent={ent_val:.3f} ovr={p_now:.2f} "
                      f"bc={bc_val:.3f}*{bcc:.2f} t={time.time()-t_start:.0f}s", flush=True)
            if upd % args.save_every == 0:
                torch.save({"state_dict": model.state_dict(), "meta": meta, "arch": arch}, args.out)
            if upd % args.snapshot_every == 0:
                snap = os.path.join(args.snapshot_dir, f"snap_s{si}_{upd:05d}.bin")
                try:
                    export_snapshot(args.out, args.meta, snap)
                    snapshots.append(snap)
                    print(f"tikitaka: + snapshot {snap} (total {len(snapshots)})", flush=True)
                except Exception as ex:
                    print(f"tikitaka: snapshot failed: {ex}", flush=True)

            advance = False
            if upd % args.eval_every == 0 or upd >= stage["max_upd"]:
                m = ship_if_best(f"s{si}:{stage['name']}:u{upd}", escn, no_score=stage.get("no_score", False))
                if upd >= stage["min_upd"] and stage.get("primary") and m:
                    pv = m.get(stage["primary"], 0.0)
                    fl = stage.get("floor", 0)
                    if stage["primary"] != "crowd_mean" and pv < fl:
                        print(f"tikitaka: STUCK? '{stage['name']}' {stage['primary']}={pv:.2f} < floor {fl} "
                              f"at upd {upd} -- fundamentals not emerging on this drill", flush=True)
                if upd >= stage["min_upd"] and stage["gate"](m):
                    advance = True
                    print(f"tikitaka: stage '{stage['name']}' GATE met at upd {upd}", flush=True)
                # re-establish stage scenarios after the eval reset
                cur = [reset_to(envs[i], env_scn[i], p_override=override_p(stage, upd)) for i in range(args.n_envs)]
                step_in_ep = [0] * args.n_envs
            if advance or upd >= stage["max_upd"] or (args.seconds and time.time() - t_start > args.seconds):
                break

        # Stage end: refresh the embedded net + parity so `-difficulty neural` tracks progress.
        try:
            export_parity(args.out + ".best" if os.path.exists(args.out + ".best") else args.out,
                          args.meta, args.ship_to)
            print(f"tikitaka: stage {si} end -> embedded {args.ship_to} (with parity)", flush=True)
        except Exception as ex:
            print(f"tikitaka: parity export failed: {ex}", flush=True)
        if args.seconds and time.time() - t_start > args.seconds:
            print("tikitaka: wall-clock budget reached; stopping", flush=True)
            break

    for e in envs:
        e.close()
    print(f"tikitaka: done; best score={best_score:.3f}", flush=True)


if __name__ == "__main__":
    main()
