"""The Deep-Sets policy-value network. Its forward mirrors the Go inference (internal/policy
Forward) STRUCTURALLY: it reconstructs the self/ball/global blocks and the variable teammate/
opponent entity rows from the flat feature vector, runs a shared per-entity encoder phi, pools
each group with a symmetric (sum, max) pool, concatenates, and runs a trunk MLP feeding factored
policy heads (plus a value head used only by PPO). The exported weights are the phi/trunk/policy
heads; Go runs them with the same block split and pooling, so behavior transfers. ReLU-only
hidden activations and linear heads keep the pure-Go forward simple and reproducible."""

import torch
import torch.nn as nn


def mlp(sizes, out_relu=True):
    layers = []
    for i in range(len(sizes) - 1):
        layers.append(nn.Linear(sizes[i], sizes[i + 1]))
        if out_relu or i < len(sizes) - 2:
            layers.append(nn.ReLU())
    return nn.Sequential(*layers)


class DeepSetsPolicy(nn.Module):
    def __init__(self, meta, phi_hidden=64, phi_out=64, trunk_hidden=256):
        super().__init__()
        self.meta = meta
        self.self_dim = meta["self_dim"]
        self.ball_dim = meta["ball_dim"]
        self.global_dim = meta["global_dim"]
        self.ent_dim = meta["ent_dim"]
        self.max_team = meta["max_teammates"]
        self.max_opp = meta["max_opponents"]
        self.head_sizes = meta["head_sizes"]
        self.phi_out = phi_out

        # Phi: two ReLU layers ent_dim -> phi_hidden -> phi_out (last layer ReLU so encodings
        # are >=0, matching Go's empty-group-pools-to-zero convention).
        self.phi = mlp([self.ent_dim, phi_hidden, phi_out], out_relu=True)

        concat = self.self_dim + self.ball_dim + self.global_dim + 4 * phi_out
        self.trunk = mlp([concat, trunk_hidden, trunk_hidden], out_relu=True)
        self.heads = nn.ModuleList([nn.Linear(trunk_hidden, s) for s in self.head_sizes])
        self.value = nn.Linear(trunk_hidden, 1)

        # Layer dims exposed for the exporter (phi layers, trunk layers, head dims).
        self.phi_dims = [(self.ent_dim, phi_hidden), (phi_hidden, phi_out)]
        self.trunk_dims = [(concat, trunk_hidden), (trunk_hidden, trunk_hidden)]

    def split(self, obs):
        m = self.meta
        b = obs.shape[0]
        i = 0
        s = obs[:, i : i + self.self_dim]; i += self.self_dim
        ball = obs[:, i : i + self.ball_dim]; i += self.ball_dim
        glob = obs[:, i : i + self.global_dim]; i += self.global_dim
        t0, t1 = m["team_region"]
        o0, o1 = m["opp_region"]
        team = obs[:, t0:t1].reshape(b, self.max_team, self.ent_dim)
        opp = obs[:, o0:o1].reshape(b, self.max_opp, self.ent_dim)
        n_team = obs[:, -2]
        n_opp = obs[:, -1]
        return s, ball, glob, team, opp, n_team, n_opp

    def _pool(self, rows, counts, max_n):
        # rows: [B, max_n, ent_dim]; counts: [B] real entity counts.
        b = rows.shape[0]
        enc = self.phi(rows)  # [B, max_n, phi_out]
        idx = torch.arange(max_n, device=rows.device).unsqueeze(0)  # [1, max_n]
        mask = idx < counts.long().unsqueeze(1)  # [B, max_n]
        m = mask.unsqueeze(-1).to(enc.dtype)
        s = (enc * m).sum(dim=1)  # sum over valid rows
        neg = torch.where(mask.unsqueeze(-1), enc, torch.full_like(enc, float("-inf")))
        mx = neg.max(dim=1).values
        any_valid = mask.any(dim=1, keepdim=True)
        mx = torch.where(any_valid, mx, torch.zeros_like(mx))  # empty group -> zeros (matches Go)
        return s, mx

    def trunk_out(self, obs):
        s, ball, glob, team, opp, n_team, n_opp = self.split(obs)
        ts, tm = self._pool(team, n_team, self.max_team)
        os_, om = self._pool(opp, n_opp, self.max_opp)
        concat = torch.cat([s, ball, glob, ts, tm, os_, om], dim=-1)
        return self.trunk(concat)

    def forward(self, obs):
        h = self.trunk_out(obs)
        logits = [head(h) for head in self.heads]
        value = self.value(h).squeeze(-1)
        return logits, value
