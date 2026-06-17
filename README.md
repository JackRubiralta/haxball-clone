# phootball

A top-down, physics-based soccer game (a Haxball-style clone) written in Go with
[Ebiten](https://ebitengine.org/). The tuned ball-dribbling physics is the heart of
the game: the ball sticks to a player's front and can be dribbled, stolen, and shot.

## Running

```sh
# Local game: you control one outfielder; AI fills both teams.
go run ./cmd/game                 # 3v3 by default
go run ./cmd/game -team-size 1     # 1v1
go run ./cmd/game -solo            # one player + ball, no opponents (testing)
go run ./cmd/game -ai-both         # spectate AI vs AI

# LAN play (server-authoritative): the server runs all the physics.
go run ./cmd/server -addr :4000             # headless host (no graphics)
go run ./cmd/client -addr HOST:4000         # each player runs a client
```

Controls: **WASD** to move, **mouse** to aim (the cone shows your facing), **Space**
to shoot.

## Architecture

The code is split into layers so the same deterministic simulation runs in the local
client and on the authoritative server. Ebiten is confined to the rendering and
human-input packages; everything else is headless (the `server` binary links no
graphics stack at all).

```
cmd/game      local single-process client + simulation
cmd/server    authoritative headless host (runs all collisions)  -- no Ebiten
cmd/client    thin client: sends intents, renders server snapshots
internal/geom     Vec and 2D vector math
internal/physics  Body + Shape (Circle/Segment), collision engine, headless
internal/sim      entities, per-player stats/roles, dribble/shoot, Match.Step
internal/control  Controller interface + role-aware AI (headless)
internal/input    human keyboard/mouse controller (Ebiten)
internal/render   all drawing (Ebiten)
internal/netcode  TCP + gob server/client, snapshots
```

Import direction is acyclic: `render/control/netcode -> sim -> physics -> geom`.

Key ideas:
- **Shapes are polymorphic** (`physics.Shape`): a player or the ball is a dynamic
  `Circle`, a fixed cone is a static circle (`InvMass == 0`), and walls/posts can be
  `Segment`s. Collision resolution is mass-weighted, so an immovable body is never
  pushed.
- **One input seam**: humans, AI, and remote network clients all produce the same
  `sim.Intent` (move direction, throttle, aim, shoot). The simulation never knows
  where input came from, which is what makes server-authoritative play possible.
- **Per-player stats** (`sim.PlayerStats` + role presets): a goalkeeper bounces less
  and is less sticky, a midfielder shoots harder, a striker is faster — all built on
  swappable angle-response curves.
