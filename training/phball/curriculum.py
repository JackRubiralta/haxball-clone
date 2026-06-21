"""From-scratch tiki-taka curriculum: ordered stages, each a distribution of drill scenarios + a
reward profile + an advance-gate on the telemetry panel. No teacher / rule-AI cloning. The trainer
(tikitaka.py) runs each stage until its gate is met or its budget is spent, shipping the best policy
by a tiki-taka telemetry score. Stages go fundamentals -> passing -> possession -> defense ->
full-game self-play -> sharpen, mirroring TiZero / DeepMind staged football curricula."""

from .env_client import (
    SCEN_KICKOFF, SCEN_SHOOTING, SCEN_RONDO, SCEN_BUILDUP, SCEN_DEFEND, SCEN_COLLECT, SCEN_CARRY,
    PROF_FULL, PROF_SHOOTING, PROF_PASSING, PROF_POSSESSION, PROF_DEFENSE,
    PROF_RECEIVE, PROF_HOLD, PROF_CARRY,
    TEACH_NONE, TEACH_COLLECTOR, TEACH_CARRIER, TEACH_TIKITAKA,
)


def scen(kind, home, away, profile, field="large", opp="scripted", episode_len=160, teacher=TEACH_NONE):
    """One drill spec. home/away are the LEARNER and OPPONENT roster sizes; opp is 'scripted' (the
    scenario's built-in keeper/presser; away=0 means no opponents) or 'frozen' (a self-snapshot).
    teacher (a TEACH_* kind) enables annealed action-override bootstrapping for the stage: a validated
    scripted demonstrator drives a fraction of episodes so the policy sees the behaviour (esp. passing)
    it would almost never discover by random exploration."""
    return dict(kind=kind, home=home, away=away, profile=profile, field=field,
                opp=opp, episode_len=episode_len, teacher=teacher)


# GENTLE fundamentals-first curriculum (the 11-agent design synthesis). The earlier collapse was a
# difficulty cliff: a from-scratch policy thrown into a pressured rondo with a hard length-gated gate
# got stripped instantly -> no learning signal. Here difficulty ramps smoothly: unopposed skills ->
# one presser -> two; roster 1 -> 2 -> 3 -> 4 -> 6; entropy decreases monotonically; EARLY GATES ARE
# SINGLE-METRIC AND SOFT. `primary`/`floor` drive a stuck-stage diagnostic. self_play uses frozen
# self-snapshots once they exist.
STAGES = [
    dict(name="motor", ent=0.012, min_upd=200, max_upd=1200, self_play=False,
         scenarios=[scen(SCEN_SHOOTING, 1, 1, PROF_SHOOTING, episode_len=140)],
         primary="shots_per_min", floor=1.0,
         gate=lambda m: m["shots_per_min"] >= 2.0 and m["goals_for_per_ep"] >= 0.2),

    dict(name="collect", ent=0.012, min_upd=200, max_upd=1200, self_play=False, no_score=True,
         scenarios=[scen(SCEN_COLLECT, 1, 0, PROF_RECEIVE, episode_len=140, teacher=TEACH_COLLECTOR)],
         primary="possession_pct", floor=0.3,  # collecting = gaining the loose ball; receive_per_min is diagnostic only
         gate=lambda m: m["possession_pct"] >= 0.6),

    dict(name="firsttouch", ent=0.011, min_upd=250, max_upd=1500, self_play=False, no_score=True,
         scenarios=[scen(SCEN_RONDO, 2, 0, PROF_PASSING, episode_len=160, teacher=TEACH_TIKITAKA)],  # 2 learners, no presser
         primary="possession_pct", floor=0.4,
         # possession-only for now (the real pass requirement lives in rondo3v1); the passing profile +
         # release-toward-mate shaping still TRAIN exchanges here, we just don't GATE on them yet.
         gate=lambda m: m["possession_pct"] >= 0.7),

    dict(name="hold", ent=0.011, min_upd=250, max_upd=1500, self_play=False, no_score=True,
         scenarios=[scen(SCEN_RONDO, 1, 1, PROF_HOLD, episode_len=180)],  # 1 learner shields vs 1 presser
         primary="possession_pct", floor=0.25,
         gate=lambda m: m["possession_pct"] >= 0.45),

    dict(name="carry", ent=0.010, min_upd=250, max_upd=1500, self_play=False,
         scenarios=[scen(SCEN_CARRY, 1, 1, PROF_CARRY, episode_len=180, teacher=TEACH_CARRIER)],
         primary="possession_pct", floor=0.3,
         gate=lambda m: m["possession_pct"] >= 0.5),

    dict(name="rondo3v1", ent=0.010, min_upd=400, max_upd=2500, self_play=False, no_score=True,
         scenarios=[scen(SCEN_RONDO, 3, 1, PROF_PASSING, episode_len=180, teacher=TEACH_TIKITAKA)],
         primary="pass_per_min", floor=2.0,
         gate=lambda m: m["possession_pct"] >= 0.5 and m["pass_per_min"] >= 5.0),

    dict(name="rondo4v2", ent=0.009, min_upd=400, max_upd=2500, self_play=False, no_score=True,
         scenarios=[scen(SCEN_RONDO, 4, 2, PROF_PASSING, episode_len=200, teacher=TEACH_TIKITAKA)],
         primary="pass_per_min", floor=3.0,
         gate=lambda m: m["possession_pct"] >= 0.5 and m["pass_per_min"] >= 6.0
         and m["real_pass_frac"] >= 0.35),

    # Self-play possession stages keep the TEACHER as a persistent BC anchor (teacher=TEACH_TIKITAKA):
    # without it the no-score possession reward is flat (dead critic, diffusing entropy) and the policy
    # UNLEARNS passing and collapses to a hoard -- observed regression. The gates also require REAL
    # passing + good shape (low crowd), so a possession-hoard can no longer pass them (possession alone
    # is gameable by collapsing the whole team onto the ball).
    dict(name="buildup", ent=0.008, min_upd=400, max_upd=2500, self_play=True, no_score=True,
         scenarios=[scen(SCEN_BUILDUP, 4, 3, PROF_POSSESSION, opp="frozen", episode_len=220, teacher=TEACH_TIKITAKA)],
         primary="pass_per_min", floor=2.0,
         gate=lambda m: m["possession_pct"] >= 0.50 and m["own_third_turnover_frac"] <= 0.35
         and m["pass_per_min"] >= 3.0 and m["crowd_mean"] <= 0.85),

    dict(name="possession", ent=0.007, min_upd=500, max_upd=3000, self_play=True, no_score=True,
         scenarios=[scen(SCEN_BUILDUP, 6, 5, PROF_POSSESSION, opp="frozen", episode_len=260, teacher=TEACH_TIKITAKA)],
         primary="pass_per_min", floor=3.0,
         gate=lambda m: m["possession_pct"] >= 0.55 and m["own_third_turnover_frac"] <= 0.30
         and m["pass_per_min"] >= 6.0 and m["crowd_mean"] <= 0.80),

    dict(name="defense", ent=0.006, min_upd=400, max_upd=2500, self_play=True,
         scenarios=[scen(SCEN_DEFEND, 6, 6, PROF_DEFENSE, opp="frozen", episode_len=220)],
         primary="crowd_mean", floor=99,  # crowd is "lower is better"; diagnostic only
         gate=lambda m: m["crowd_mean"] <= 2.6 and m["gk_box_mean"] <= 0.6
         and m["stay_back_frac"] >= 0.25),

    dict(name="fullgame", ent=0.004, min_upd=1500, max_upd=9000, self_play=True,
         scenarios=[scen(SCEN_KICKOFF, 6, 6, PROF_FULL, opp="frozen", episode_len=300)],
         primary="possession_pct", floor=0.35,
         gate=lambda m: False),  # train to budget -- the real strength comes from here

    dict(name="sharpen", ent=0.001, min_upd=400, max_upd=1800, self_play=True,
         scenarios=[scen(SCEN_KICKOFF, 6, 6, PROF_FULL, opp="frozen", episode_len=300)],
         primary="possession_pct", floor=0.4,
         gate=lambda m: False),  # low-entropy crispening of the deterministic policy
]


def metrics_from_telemetry(teles):
    """Aggregate a list of per-episode telemetry dicts into rate/fraction metrics. 60 ticks/sec."""
    if not teles:
        return {}
    k = lambda key: sum(t.get(key, 0) for t in teles)
    karr = lambda key, i: sum(t.get(key, [0, 0, 0])[i] for t in teles)
    eps = len(teles)
    ticks = max(k("total_ticks"), 1)
    minutes = ticks / 3600.0
    poss = k("possession_ticks")
    opp_poss = k("opp_possession_ticks")
    passes = k("passes")
    our_turnovers = karr("turnover_by_third", 0) + karr("turnover_by_third", 1) + karr("turnover_by_third", 2)
    med_long = karr("pass_buckets", 1) + karr("pass_buckets", 2)
    return dict(
        episodes=eps,
        possession_pct=poss / max(poss + opp_poss, 1),
        pass_per_min=passes / max(minutes, 1e-6),
        real_pass_frac=med_long / max(passes, 1),
        progressive_frac=k("progressive_passes") / max(passes, 1),
        mean_pass_len=k("pass_len_sum") / max(passes, 1),
        short_poke_per_min=k("short_pokes") / max(minutes, 1e-6),
        own_third_turnover_frac=karr("turnover_by_third", 0) / max(our_turnovers, 1),
        turnover_per_min=our_turnovers / max(minutes, 1e-6),
        regains_per_min=k("regains") / max(minutes, 1e-6),
        dawdle_frac=k("dawdle_ticks") / max(poss, 1),
        crowd_mean=k("crowd_sum") / ticks,
        gk_box_mean=k("gk_box_sum") / ticks,
        stay_back_frac=k("stay_back_ticks") / ticks,
        shots_per_min=k("shots") / max(minutes, 1e-6),
        sot_frac=k("shots_on_target") / max(k("shots"), 1),
        push_per_min=k("pushes") / max(minutes, 1e-6),
        trap_frac=k("trap_ticks") / ticks,
        receive_per_min=k("receive_attempts") / max(minutes, 1e-6),
        receive_clean_frac=k("receive_clean") / max(k("receive_attempts"), 1),
        collections_per_min=k("collections") / max(minutes, 1e-6),
        pass_attempt_per_min=k("pass_attempts") / max(minutes, 1e-6),
        goals_for_per_ep=k("goals_for") / eps,
        goals_against_per_ep=k("goals_against") / eps,
        goal_diff_per_ep=(k("goals_for") - k("goals_against")) / eps,
    )


def _clip01(x):
    return 0.0 if x < 0 else (1.0 if x > 1 else x)


def tikitaka_score(m, no_score=False):
    """A single scalar for ship-best: rewards possession, real (length-gated) passing, keeping the
    ball out of our own third, good shape (no crowding, a player back, box clear), use of all
    abilities incl. push, and finishing -- the qualities the user asked for. NOT goals-only.

    no_score=True drops the finishing/goal-diff term entirely (and re-weights toward passing) for the
    keep-away / possession drills, where scoring is NOT the objective. This is the eval-side half of
    the rondo fix: without it, a policy that just boots the ball into the net would keep shipping
    marginal NEW-BESTs on goal-diff luck while pass_per_min stayed pinned at 0."""
    if not m:
        return -1.0
    possession = m["possession_pct"]
    passing = _clip01(m["pass_per_min"] / 12.0) * m["real_pass_frac"]
    keep = 1.0 - _clip01(m["own_third_turnover_frac"])
    shape = (_clip01(1 - (m["crowd_mean"] - 2.0) / 3.0) * 0.5
             + _clip01(1 - m["gk_box_mean"]) * 0.25
             + _clip01(m["stay_back_frac"] / 0.4) * 0.25)
    abilities = 0.5 * _clip01(m["push_per_min"] / 4.0) + 0.5 * _clip01(m["trap_frac"] / 0.15)
    if no_score:
        # Keep-away/possession: reward only retention + real passing + keep + shape + abilities. The
        # 0.12 finishing weight is redistributed onto possession and passing (the actual lessons).
        return (0.34 * possession + 0.30 * passing + 0.14 * keep + 0.14 * shape
                + 0.08 * abilities)
    finishing = _clip01(m["shots_per_min"] / 4.0) * 0.6 + _clip01((m["goal_diff_per_ep"] + 1) / 2) * 0.4
    return (0.28 * possession + 0.22 * passing + 0.14 * keep + 0.14 * shape
            + 0.10 * abilities + 0.12 * finishing)
