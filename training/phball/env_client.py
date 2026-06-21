"""Python client for the cmd/env binary IPC (length-prefixed binary protocol). One EnvClient is
one Go subprocess running one match; VecEnv fans many out to feed a single GPU learner. Go owns
featurization, decode, opponents, and reward; we send action indices and receive obs/reward/done/
mask per controlled agent."""

import json
import struct
import subprocess

import numpy as np

OP_RESET, OP_STEP, OP_OBS, OP_CLOSE, OP_CLOSED = 0x01, 0x02, 0x03, 0x04, 0x05
OP_SCENARIO, OP_TELEMETRY, OP_TELEOUT = 0x06, 0x07, 0x08

FIELD = {"small": 0, "medium": 1, "large": 2}
OPP_RULE = {"easy": 0, "normal": 1, "hard": 2, "impossible": 3}
OPP_FROZEN = 4

# Scenario kinds (mirror internal/scenario/scenario.go).
SCEN_KICKOFF, SCEN_SHOOTING, SCEN_RONDO, SCEN_BUILDUP, SCEN_DEFEND = 0, 1, 2, 3, 4
SCEN_COLLECT, SCEN_CARRY = 5, 6
# Reward profiles (mirror cmd/env/reward.go profileByID).
PROF_FULL, PROF_SHOOTING, PROF_PASSING, PROF_POSSESSION, PROF_DEFENSE = 0, 1, 2, 3, 4
PROF_RECEIVE, PROF_HOLD, PROF_CARRY = 5, 6, 7
# Scripted teacher kinds for guided bootstrapping (mirror internal/scenario.ScriptKind). 0 = none.
TEACH_NONE, TEACH_COLLECTOR, TEACH_CARRIER, TEACH_TIKITAKA = 0, 3, 4, 5


class EnvClient:
    def __init__(self, env_bin, flat_dim, total_logits, num_heads=5):
        self.p = subprocess.Popen([env_bin], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
        self.flat = flat_dim
        self.total_logits = total_logits
        self.num_heads = num_heads
        self.mask_bytes = (total_logits + 7) // 8
        self.n_agents = 0

    def _send(self, payload):
        self.p.stdin.write(struct.pack("<I", len(payload)))
        self.p.stdin.write(payload)
        self.p.stdin.flush()

    def _recv(self):
        hdr = self.p.stdout.read(4)
        if len(hdr) < 4:
            raise EOFError("env closed")
        (n,) = struct.unpack("<I", hdr)
        buf = b""
        while len(buf) < n:
            chunk = self.p.stdout.read(n - len(buf))
            if not chunk:
                raise EOFError("env closed mid-message")
            buf += chunk
        return buf

    def reset(self, team_size, field, offside, frame_skip, seed, ctrl_side, opp, frozen_path=""):
        field_idx = FIELD[field] if isinstance(field, str) else int(field)
        if opp in OPP_RULE:
            opp_kind = OPP_RULE[opp]
            extra = b""
        elif opp in ("frozen", "snapshot"):
            opp_kind = OPP_FROZEN
            pb = frozen_path.encode()
            extra = struct.pack("<H", len(pb)) + pb
        else:
            raise ValueError(f"unknown opponent {opp}")
        payload = bytes([OP_RESET, team_size, field_idx, 1 if offside else 0, frame_skip]) \
            + struct.pack("<q", int(seed)) + bytes([ctrl_side, opp_kind]) + extra
        self._send(payload)
        return self._parse_obs(self._recv())

    def reset_scenario(self, kind, home, away, field, offside, frame_skip, profile,
                       ctrl_side, opp, seed, episode_len, frozen_path="",
                       teacher=0, p_override=0.0):
        """Reset to a drill scenario (see cmd/env/scenario.go). opp is a rule name (sparring only),
        'frozen'/'snapshot' for a self-snapshot, or 'scripted' to let the scenario pick its scripted
        actor (keeper/presser). teacher (a TEACH_* kind) + p_override enable guided bootstrapping: with
        probability p_override this episode is driven by the scripted teacher and the EXECUTED action
        indices come back in obs['exec'] so PPO imitates the teacher."""
        field_idx = FIELD[field] if isinstance(field, str) else int(field)
        if opp in OPP_RULE:
            opp_kind, extra = OPP_RULE[opp], b""
        elif opp in ("frozen", "snapshot"):
            opp_kind = OPP_FROZEN
            pb = frozen_path.encode()
            extra = struct.pack("<H", len(pb)) + pb
        else:  # 'scripted' / anything else: Go uses the scenario's scripted actor
            opp_kind, extra = 0, b""
        payload = bytes([OP_SCENARIO, kind, home, away, field_idx, 1 if offside else 0,
                         frame_skip, profile, ctrl_side, opp_kind]) \
            + struct.pack("<q", int(seed)) + struct.pack("<I", int(episode_len)) \
            + struct.pack("<B", int(teacher)) + struct.pack("<f", float(p_override)) + extra
        self._send(payload)
        return self._parse_obs(self._recv())

    def telemetry(self):
        """Request the per-episode tiki-taka telemetry panel (JSON dict)."""
        self._send(bytes([OP_TELEMETRY]))
        b = self._recv()
        assert b[0] == OP_TELEOUT, f"opcode {b[0]:#x}"
        return json.loads(bytes(b[1:]))

    def step_send(self, actions):
        """Send a STEP without waiting for the reply, so many env workers compute in parallel.
        actions: array-like [n_agents, num_heads] of int indices in agent (sorted-ID) order."""
        payload = bytearray([OP_STEP])
        for a in actions:
            for h in a:
                payload += struct.pack("<i", int(h))
        self._send(bytes(payload))

    def step_recv(self):
        return self._parse_obs(self._recv())

    def step(self, actions):
        self.step_send(actions)
        return self.step_recv()

    def close(self):
        try:
            self._send(bytes([OP_CLOSE]))
            self._recv()
        except Exception:
            pass
        try:
            self.p.wait(timeout=5)
        except Exception:
            self.p.kill()

    def _parse_obs(self, b):
        assert b[0] == OP_OBS, f"opcode {b[0]:#x}"
        off = 1
        (n,) = struct.unpack_from("<H", b, off); off += 2
        self.n_agents = n
        ids = np.empty(n, dtype=np.int32)
        obs = np.empty((n, self.flat), dtype=np.float32)
        rew = np.empty(n, dtype=np.float32)
        done = np.empty(n, dtype=np.float32)
        mask = np.zeros((n, self.total_logits), dtype=bool)
        exec_idx = np.zeros((n, self.num_heads), dtype=np.int64)
        advice = np.full((n, self.num_heads), -1, dtype=np.int64)
        for i in range(n):
            (ids[i],) = struct.unpack_from("<i", b, off); off += 4
            obs[i] = np.frombuffer(b, dtype="<f4", count=self.flat, offset=off); off += 4 * self.flat
            (rew[i],) = struct.unpack_from("<f", b, off); off += 4
            done[i] = b[off]; off += 1
            (mlen,) = struct.unpack_from("<H", b, off); off += 2
            mbytes = b[off:off + mlen]; off += mlen
            bits = np.unpackbits(np.frombuffer(mbytes, dtype=np.uint8), bitorder="little")
            mask[i] = bits[:self.total_logits].astype(bool)
            for h in range(self.num_heads):
                (exec_idx[i, h],) = struct.unpack_from("<i", b, off); off += 4
            for h in range(self.num_heads):
                (advice[i, h],) = struct.unpack_from("<i", b, off); off += 4
        (tick,) = struct.unpack_from("<I", b, off); off += 4
        return dict(ids=ids, obs=obs, reward=rew, done=done, mask=mask, exec=exec_idx,
                    advice=advice, tick=tick)


class VecEnv:
    """A fixed bank of EnvClients with identical agent counts, stacked for batched stepping."""

    def __init__(self, env_bin, flat_dim, total_logits, n_envs):
        self.envs = [EnvClient(env_bin, flat_dim, total_logits) for _ in range(n_envs)]
        self.n_envs = n_envs

    def reset_all(self, **kw):
        return [e.reset(**kw) for e in self.envs]

    def close(self):
        for e in self.envs:
            e.close()
