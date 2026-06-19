"""Memory-mapped reader over datagen shards. Each shard is a 64-byte header followed by
fixed-stride records of (feature_dim float32 obs, num_heads int32 labels). Train/val/test split
is BY SHARD (seed/size/ruleset), never by frame, so temporally-correlated frames cannot leak
across the split."""

import glob
import os

import numpy as np
import torch
from torch.utils.data import Dataset

from .meta import HEADER_SIZE, read_shard_header


def list_shards(shard_dir):
    return sorted(glob.glob(os.path.join(shard_dir, "*.bin")))


def _split_of(hdr, val_every=10):
    """Assign a whole shard to train/val by its start seed, never by frame. Shards are generated
    with start seeds spaced by 10 (0,10,20,...); ~1/val_every of those buckets become validation.
    The eval grid (cmd/eval) uses its own low seeds (0..29) disjoint from the training seeds, so
    behavioral evaluation never overlaps the training distribution."""
    bucket = (hdr["seed"] // 10) % val_every
    return "val" if bucket == 0 else "train"


def load_split_arrays(shard_dir, split, feature_dim, num_heads, val_every=10):
    """Preload an entire split into two contiguous RAM arrays (obs float32 [N,F], labels int64
    [N,H]). Sequential per-shard reads + a manual minibatch loop make training GPU-bound rather
    than memmap-random-access-bound. Memory: ~N*F*4 bytes for obs (preallocated, no concat
    doubling)."""
    rec_dtype = np.dtype([("obs", "<f4", (feature_dim,)), ("lab", "<i4", (num_heads,))])
    chosen = []
    total = 0
    for path in list_shards(shard_dir):
        with open(path, "rb") as f:
            hdr = read_shard_header(f.read(HEADER_SIZE))
        if hdr["feature_dim"] != feature_dim or hdr["num_heads"] != num_heads:
            raise ValueError(f"{path}: header dims != meta")
        if _split_of(hdr, val_every) != split:
            continue
        chosen.append((path, hdr["count"]))
        total += hdr["count"]
    obs = np.empty((total, feature_dim), dtype=np.float32)
    lab = np.empty((total, num_heads), dtype=np.int64)
    off = 0
    for path, count in chosen:
        mm = np.memmap(path, dtype=rec_dtype, mode="r", offset=HEADER_SIZE, shape=(count,))
        obs[off:off + count] = mm["obs"]
        lab[off:off + count] = mm["lab"]
        off += count
        del mm
    return obs, lab


class ShardDataset(Dataset):
    """A flat view over a set of shards' records, selected by split."""

    def __init__(self, shard_dir, split, feature_dim, num_heads, val_every=10):
        self.feature_dim = feature_dim
        self.num_heads = num_heads
        self.rec_dtype = np.dtype([("obs", "<f4", (feature_dim,)), ("lab", "<i4", (num_heads,))])
        self.maps = []          # one memmap per selected shard
        self.index = []         # (map_idx, record_idx)
        for path in list_shards(shard_dir):
            with open(path, "rb") as f:
                hdr = read_shard_header(f.read(HEADER_SIZE))
            if hdr["feature_dim"] != feature_dim or hdr["num_heads"] != num_heads:
                raise ValueError(f"{path}: header dims {hdr['feature_dim']}/{hdr['num_heads']} "
                                 f"!= meta {feature_dim}/{num_heads}")
            if _split_of(hdr, val_every) != split:
                continue
            mm = np.memmap(path, dtype=self.rec_dtype, mode="r", offset=HEADER_SIZE,
                           shape=(hdr["count"],))
            mi = len(self.maps)
            self.maps.append(mm)
            self.index.extend((mi, r) for r in range(hdr["count"]))

    def __len__(self):
        return len(self.index)

    def __getitem__(self, i):
        mi, r = self.index[i]
        rec = self.maps[mi][r]
        obs = torch.from_numpy(np.array(rec["obs"], dtype=np.float32))
        lab = torch.from_numpy(np.array(rec["lab"], dtype=np.int64))
        return obs, lab

    def ability_histogram(self, ability_head=3, ability_size=4, sample=200000):
        """Counts of each ability label over a capped random sample, for class weighting."""
        counts = np.zeros(ability_size, dtype=np.int64)
        n = len(self.index)
        if n == 0:
            return counts
        step = max(1, n // sample)
        for i in range(0, n, step):
            mi, r = self.index[i]
            counts[int(self.maps[mi][r]["lab"][ability_head])] += 1
        return counts
