"""Export a trained DeepSetsPolicy to the Go `PHNNW1` weight file, and emit bit-exact parity
vectors. The parity reference is computed with a SEQUENTIAL float32 reduction that mirrors
internal/policy.Dense.applyInto exactly (left-to-right accumulation, float32 throughout, ReLU,
sum/max pooling) -- NOT numpy's pairwise dot -- so the Go forward matches it bit-for-bit. This is
the contract that guarantees "Go inference == the exported net" under the project's defined
float32 semantics."""

import argparse
import struct

import numpy as np
import torch

from .meta import load_meta
from .model import DeepSetsPolicy

MAGIC = b"PHNNW1\x00\x00"
FORMAT_VERSION = 1
ARCH_DEEPSETS_V1 = 1


def linear_wb(layer):
    # torch Linear.weight is [out, in]; flattened C-order == Go's out-major W[o*in+i]. Force <f4.
    w = layer.weight.detach().cpu().numpy().astype("<f4")
    b = layer.bias.detach().cpu().numpy().astype("<f4")
    return w, b


def collect_layers(model):
    """Return (phi, trunk, heads) as lists of (W[out,in], B[out]) float32 arrays, in file order."""
    phi = [linear_wb(m) for m in model.phi if isinstance(m, torch.nn.Linear)]
    trunk = [linear_wb(m) for m in model.trunk if isinstance(m, torch.nn.Linear)]
    heads = [linear_wb(h) for h in model.heads]
    return phi, trunk, heads


def write_weights(path, meta, phi, trunk, heads, phi_out):
    out = bytearray()
    out += MAGIC
    out += struct.pack("<II", FORMAT_VERSION, ARCH_DEEPSETS_V1)
    out += struct.pack("<IIIII", meta["ent_dim"], meta["self_dim"], meta["ball_dim"],
                       meta["global_dim"], phi_out)
    out += struct.pack("<III", len(phi), len(trunk), len(heads))
    for hs in meta["head_sizes"]:
        out += struct.pack("<I", hs)
    # Layer table: (in, out) per layer in phi, trunk, head order.
    for w, b in phi + trunk + heads:
        o, i = w.shape
        out += struct.pack("<II", i, o)
    # Blob: W (out-major, row-major) then B, per layer.
    for w, b in phi + trunk + heads:
        out += w.tobytes()  # already <f4, C-order [out, in] == out-major
        out += b.tobytes()
    with open(path, "wb") as f:
        f.write(out)
    return len(out)


# ---- Sequential float32 reference (mirrors internal/policy exactly) ----

def seq_dense(w, b, x, relu):
    o_dim, i_dim = w.shape
    out = np.empty(o_dim, dtype=np.float32)
    for o in range(o_dim):
        acc = np.float32(0.0)
        row = w[o]
        for i in range(i_dim):
            acc = np.float32(acc + np.float32(row[i]) * np.float32(x[i]))
        acc = np.float32(acc + b[o])
        if relu and acc < 0:
            acc = np.float32(0.0)
        out[o] = acc
    return out


def seq_mlp(layers, x, relu_last):
    for k, (w, b) in enumerate(layers):
        relu = relu_last or k < len(layers) - 1
        x = seq_dense(w, b, x, relu)
    return x


def seq_forward(meta, phi, trunk, heads, phi_out, self_v, ball_v, glob_v, team_rows, opp_rows):
    def pool(rows):
        s = np.zeros(phi_out, dtype=np.float32)
        mx = np.zeros(phi_out, dtype=np.float32)
        for row in rows:
            e = seq_mlp(phi, np.asarray(row, dtype=np.float32), relu_last=True)
            for j in range(phi_out):
                s[j] = np.float32(s[j] + e[j])
                if e[j] > mx[j]:
                    mx[j] = e[j]
        return s, mx

    ts, tm = pool(team_rows)
    os_, om = pool(opp_rows)
    concat = np.concatenate([
        np.asarray(self_v, dtype=np.float32),
        np.asarray(ball_v, dtype=np.float32),
        np.asarray(glob_v, dtype=np.float32),
        ts, tm, os_, om,
    ]).astype(np.float32)
    t = seq_mlp(trunk, concat, relu_last=True)
    logits = [seq_dense(w, b, t, relu=False) for (w, b) in heads]
    return np.concatenate(logits).astype(np.float32)


def write_parity(path, meta, phi, trunk, heads, phi_out, n=12, seed=12345):
    rng = np.random.default_rng(seed)
    ent = meta["ent_dim"]

    def rnd(k):
        return rng.uniform(-1, 1, size=k).astype(np.float32)

    # A mix of counts incl. empty and max groups, plus an all-zero edge case.
    cases = [(0, 0), (1, 1), (meta["max_teammates"], meta["max_opponents"]), (3, 4)]
    while len(cases) < n:
        cases.append((int(rng.integers(0, meta["max_teammates"] + 1)),
                      int(rng.integers(0, meta["max_opponents"] + 1))))
    out = bytearray()
    out += struct.pack("<I", len(cases))
    for ci, (nt, no) in enumerate(cases):
        zero = (ci == n - 1)
        self_v = np.zeros(meta["self_dim"], np.float32) if zero else rnd(meta["self_dim"])
        ball_v = np.zeros(meta["ball_dim"], np.float32) if zero else rnd(meta["ball_dim"])
        glob_v = np.zeros(meta["global_dim"], np.float32) if zero else rnd(meta["global_dim"])
        team = [np.zeros(ent, np.float32) if zero else rnd(ent) for _ in range(nt)]
        opp = [np.zeros(ent, np.float32) if zero else rnd(ent) for _ in range(no)]
        logits = seq_forward(meta, phi, trunk, heads, phi_out, self_v, ball_v, glob_v, team, opp)
        out += struct.pack("<II", nt, no)
        out += self_v.astype("<f4").tobytes()
        out += ball_v.astype("<f4").tobytes()
        out += glob_v.astype("<f4").tobytes()
        for r in team:
            out += r.astype("<f4").tobytes()
        for r in opp:
            out += r.astype("<f4").tobytes()
        out += logits.astype("<f4").tobytes()
    with open(path, "wb") as f:
        f.write(out)
    return len(cases)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--checkpoint", required=True)
    ap.add_argument("--meta", required=True)
    ap.add_argument("--weights-out", default="internal/policy/weights/neural_v1.bin")
    ap.add_argument("--parity-out", default="internal/policy/testdata/golden_forward.bin")
    ap.add_argument("--no-parity", action="store_true",
                    help="skip the (slow) parity-vector generation; for PPO snapshots used only as opponents")
    args = ap.parse_args()

    meta = load_meta(args.meta)
    ckpt = torch.load(args.checkpoint, map_location="cpu", weights_only=False)
    arch = ckpt.get("arch", {"phi_hidden": 64, "phi_out": 64, "trunk_hidden": 256})
    model = DeepSetsPolicy(meta, **arch)
    model.load_state_dict(ckpt["state_dict"])
    model.eval()

    phi, trunk, heads = collect_layers(model)
    phi_out = arch["phi_out"]
    nbytes = write_weights(args.weights_out, meta, phi, trunk, heads, phi_out)
    if args.no_parity:
        print(f"export: wrote {args.weights_out} ({nbytes} bytes), parity skipped")
    else:
        ncases = write_parity(args.parity_out, meta, phi, trunk, heads, phi_out)
        print(f"export: wrote {args.weights_out} ({nbytes} bytes), {args.parity_out} ({ncases} parity vectors)")


if __name__ == "__main__":
    main()
