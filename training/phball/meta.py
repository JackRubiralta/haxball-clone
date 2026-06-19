"""Feature/action contract loaded from dataset_meta.json (written by `go run ./cmd/datagen
-dump-meta`) plus the binary shard-header parser. Go is the single source of truth for the
feature layout; Python only reads the float32/int32 arrays. Keep this in lockstep with
internal/control/neural and cmd/datagen."""

import json
import struct

SHARD_MAGIC = b"PHBLDAT1"
HEADER_SIZE = 64


def load_meta(path):
    with open(path) as f:
        m = json.load(f)
    # Derived offsets into the flat feature vector.
    m["team_region"] = (
        m["self_dim"] + m["ball_dim"] + m["global_dim"],
        m["self_dim"] + m["ball_dim"] + m["global_dim"] + m["max_teammates"] * m["ent_dim"],
    )
    m["opp_region"] = (
        m["team_region"][1],
        m["team_region"][1] + m["max_opponents"] * m["ent_dim"],
    )
    # The trailer (last two floats) carries the real entity counts.
    assert m["opp_region"][1] == m["feature_dim"] - 2, (
        f"layout mismatch: opp end {m['opp_region'][1]} != feature_dim-2 {m['feature_dim'] - 2}"
    )
    return m


def read_shard_header(buf):
    if buf[:8] != SHARD_MAGIC:
        raise ValueError("bad shard magic")
    version, feat_dim = struct.unpack_from("<II", buf, 8)
    num_heads = buf[16]
    head_sizes = list(buf[17 : 17 + num_heads])
    (count,) = struct.unpack_from("<Q", buf, 32)
    (stride,) = struct.unpack_from("<I", buf, 40)
    (seed,) = struct.unpack_from("<q", buf, 44)
    team_l, team_r = struct.unpack_from("<II", buf, 52)
    (flags,) = struct.unpack_from("<I", buf, 60)
    return dict(
        version=version,
        feature_dim=feat_dim,
        num_heads=num_heads,
        head_sizes=head_sizes,
        count=count,
        stride=stride,
        seed=seed,
        team_l=team_l,
        team_r=team_r,
        flags=flags,
        field_preset=(flags >> 2) & 0x3,
        offside=bool(flags & 1),
        boxcaps=bool(flags & 2),
    )
