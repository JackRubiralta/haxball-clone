"""A small PFSP opponent league: rule-AI anchors (easy..impossible) plus frozen self-play
snapshots. Sampling is weighted toward opponents the learner currently beats ~50% of the time
(p*(1-p)), which keeps the curriculum challenging and prevents strategy collapse; the rule
anchors guarantee a skill floor and that the net never forgets how to beat the teacher."""

import os
import random


class League:
    # Fixed sampling weight per rule anchor — biased toward the strong tiers we must beat, so the
    # learner keeps training against them instead of drifting into pure self-play as snapshots pile
    # up (the cause of the rule-AI win-rate plateau).
    ANCHOR_WEIGHT = {"easy": 0.15, "normal": 0.35, "hard": 0.35, "impossible": 0.15}

    def __init__(self, snapshot_dir, anchors=("easy", "normal", "hard", "impossible"), anchor_frac=0.5):
        self.snapshot_dir = snapshot_dir
        os.makedirs(snapshot_dir, exist_ok=True)
        self.opponents = [dict(type="rule", spec=a, wins=0.0, games=0) for a in anchors]
        self.anchor_idx = list(range(len(anchors)))
        self.anchor_frac = anchor_frac  # fraction of episodes played against a rule anchor

    def add_snapshot(self, path):
        self.opponents.append(dict(type="frozen", spec=path, wins=0.0, games=0))

    def _pfsp(self, idxs):
        w = []
        for i in idxs:
            o = self.opponents[i]
            p = o["wins"] / o["games"] if o["games"] > 0 else 0.5
            w.append(p * (1 - p) + 0.05)  # PFSP: peak at 50%, small floor for unseen
        return random.choices(idxs, weights=w)[0]

    def sample(self):
        snaps = [i for i, o in enumerate(self.opponents) if o["type"] == "frozen"]
        if snaps and random.random() >= self.anchor_frac:
            return self._pfsp(snaps)  # self-play against a PFSP-weighted past snapshot
        # A rule anchor, weighted toward the strong tiers we must beat.
        w = [self.ANCHOR_WEIGHT.get(self.opponents[i]["spec"], 0.25) for i in self.anchor_idx]
        return random.choices(self.anchor_idx, weights=w)[0]

    def record(self, idx, result):  # result in [0,1] (1=learner win, 0.5=draw, 0=loss)
        o = self.opponents[idx]
        o["games"] += 1
        o["wins"] += result

    def spec(self, idx):
        o = self.opponents[idx]
        return (o["type"], o["spec"])

    def summary(self):
        return [
            f"{o['type']}:{os.path.basename(str(o['spec']))} "
            f"{(o['wins'] / o['games']):.2f}@{o['games']}" if o["games"] else
            f"{o['type']}:{os.path.basename(str(o['spec']))} -"
            for o in self.opponents
        ]
