package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

// trip is one walk to an unvisited chunk.
type trip struct {
	target   explore.Chunk
	started  time.Time
	lastPos  explore.Pos
	lastMove time.Time
}

const (
	tripTimeout = 90 * time.Second
	stuckAfter  = 15 * time.Second
	arriveDist  = 6.0
)

// position reads the bot's coordinates from pathStatus, and whether it is walking somewhere.
func (a *Agent) position(ctx context.Context) (explore.Pos, bool, error) {
	text, err := a.mc.Call(ctx, "pathStatus", nil)
	if err != nil {
		return explore.Pos{}, false, err
	}
	st := struct {
		Pos    string `json:"pos"`
		Busy   bool   `json:"busy"`
		Manual int64  `json:"manualMs"`
	}{Manual: -1} // an older fork jar does not report it
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		return explore.Pos{}, false, err
	}
	a.manualMs = st.Manual
	pos, err := explore.ParsePos(st.Pos)
	return pos, st.Busy, err
}

const stuckFor = 6 * time.Second

// handsOffFor is how long the bot keeps out of the way after the player last steered by hand.
const handsOffFor = 5 * time.Second // a few seconds with no WASD: he walks on his own again

// checkLobby holds the bot still in adventure mode: on a server that is the lobby with the
// login prompt, where walking and flights only get in the player's way (and the password is
// the player's to type). Once the server moves the bot into the world, it starts afresh there.
func (a *Agent) checkLobby(ctx context.Context) bool {
	if a.gameMode == "adventure" {
		if !a.lobbyHeld {
			a.lobbyHeld = true
			a.log.Info("adventure mode (a server lobby?): holding still")
			if a.trip != nil || a.outing != nil || a.activity != nil || a.flying {
				if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
					a.log.Warn("lobby stop", "err", err)
				}
			}
			a.trip, a.outing, a.activity, a.leading, a.retryFlight = nil, nil, nil, false, nil
		}
		return true
	}
	if a.lobbyHeld && a.modeKnown {
		a.lobbyHeld = false
		a.log.Info("out of the lobby", "mode", a.gameMode)
		a.resetModState() // a new world: policy, mode and the flight kit again
	}
	return false
}

// checkHands notices the player steering this account by hand: it stops the bot's own walk
// once and then holds every autonomous move (walks, flights, activities, follow) until the
// mouse and the keys have been quiet for a while. Chat answers still go on.
func (a *Agent) checkHands(ctx context.Context) bool {
	hands := a.manualMs >= 0 && a.manualMs < handsOffFor.Milliseconds()
	if !hands {
		if a.handHeld {
			a.handHeld = false
			a.log.Info("hands off: back to own moves")
		}
		return false
	}
	if !a.handHeld {
		a.handHeld = true
		a.log.Info("player steers by hand: keeping out of the way") // a follow resumes when the hands are off
		if a.trip != nil || a.outing != nil || a.activity != nil || a.leading || a.following != "" {
			if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
				a.log.Warn("hands stop", "err", err)
			}
		}
		a.trip, a.outing, a.activity, a.leading, a.retryFlight = nil, nil, nil, false, nil
	}
	if a.flying {
		a.checkFlight(ctx) // the mod lets go of the flight itself ("by hand"): note it
	}
	return true
}

// walkTarget is where the current walk is heading, if any.
func (a *Agent) walkTarget() (int, int, bool) {
	switch {
	case a.outing != nil:
		return a.outing.place.X, a.outing.place.Z, true
	case a.activity != nil:
		return a.activity.x, a.activity.z, true
	case a.trip != nil:
		x, z := a.trip.target.Center()
		return x, z, true
	case a.leading && a.places.Home != nil:
		return explore.Block(a.places.Home.X), explore.Block(a.places.Home.Z), true
	}
	return 0, 0, false
}

// checkStuck notices a walk that makes no progress: busy, yet the bot has not moved.
// It stops the walk and hops a few blocks aside, out of the corner or pit.
func (a *Agent) checkStuck(ctx context.Context, pos explore.Pos, busy bool) bool {
	// Following keeps the mod "busy" even while standing next to the target, so only walks to
	// a point (exploration, outing, leading home) are checked.
	walkingToPoint := a.trip != nil || a.outing != nil || a.leading || a.activity != nil
	// Following is checked too, but only when the one followed is out of reach (see below).
	following := a.following != "" && !walkingToPoint && !a.flying
	// The pathfinder gave up (it stops the walk as it does, so "busy" is already false): stuck now.
	gaveUp := (walkingToPoint || following) && time.Since(a.navGaveUp) < 3*time.Second
	if !gaveUp && (!busy || !(walkingToPoint || following) || math.Hypot(pos.X-a.stillPos.X, pos.Z-a.stillPos.Z) > 1 || math.Abs(pos.Y-a.stillPos.Y) > 1) {
		a.stillPos, a.stillSince = pos, time.Now()
		return false
	}
	if !gaveUp && time.Since(a.stillSince) < stuckFor {
		return false
	}
	if gaveUp {
		a.log.Info("stuck: the pathfinder gave up")
	}
	a.navGaveUp = time.Time{}
	tx, tz, haveTarget := a.walkTarget()
	if following {
		_, lx, lz, d, ok := a.playerPos(ctx, a.following)
		if !ok || d < 8 { // standing next to them is no trouble
			a.stillPos, a.stillSince = pos, time.Now()
			return false
		}
		tx, tz, haveTarget = int(lx), int(lz), true
	}
	if time.Since(a.lastStuck) > 90*time.Second {
		a.stuckCount = 0
	}
	a.stuckCount++
	a.lastStuck = time.Now()
	// In creative, walls and fences we may not break are simply flown over; and a pit or corner
	// the bot could not walk out of once is flown out of the second time.
	far := haveTarget && math.Hypot(float64(tx)-pos.X, float64(tz)-pos.Z) > a.flyAbove()
	if a.creative && (far || a.stuckCount >= 2) {
		fx, fz := flyOutTarget(pos, tx, tz, haveTarget)
		if a.startFlight(ctx, fx, fz) {
			a.log.Info("stuck: flying out", "x", explore.Block(pos.X), "z", explore.Block(pos.Z), "to_x", fx, "to_z", fz, "times", a.stuckCount)
			a.trip, a.leading = nil, false // the outing/activity keeps its target; flight lands near it
			if following {
				a.followAfter = a.following // after landing: keep up with them on foot
			}
			a.stillPos, a.stillSince = pos, time.Now()
			return true
		}
	}
	if following {
		a.stillPos, a.stillSince = pos, time.Now()
		return false // walking: the follow goes on, the mod re-plans
	}
	angle := rand.Float64() * 2 * math.Pi
	dist := 6 + rand.Float64()*4
	x, z := explore.Block(pos.X+dist*math.Cos(angle)), explore.Block(pos.Z+dist*math.Sin(angle))
	a.log.Info("stuck: hopping aside", "x", explore.Block(pos.X), "z", explore.Block(pos.Z), "to_x", x, "to_z", z)
	a.outing, a.trip, a.following, a.leading, a.activity = nil, nil, "", false, nil
	for _, cmd := range []string{"@stop", fmt.Sprintf("@goto %d %d", x, z)} {
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": cmd}); err != nil {
			a.log.Warn("unstick", "cmd", cmd, "err", err)
		}
	}
	a.stillPos, a.stillSince = pos, time.Now()
	return true
}

// roam advances exploration by one step. It only runs while nobody needs the bot:
// not following anyone and no mention for ExploreAfter.
func (a *Agent) roam(ctx context.Context) {
	pos, busy, err := a.position(ctx)
	if err != nil {
		// In the main menu the mod answers with an empty position: say so once.
		if !a.outOfWorld {
			a.outOfWorld = true
			a.log.Info("not in a world yet", "err", err)
		}
		a.havePos = false
		return
	}
	if a.outOfWorld {
		a.outOfWorld = false
		a.log.Info("in a world")
		a.resetModState() // a new world or server: policy, mode and the flight kit again
	}
	a.pos, a.havePos = pos, true
	if a.leading && a.places.Home != nil && math.Hypot(pos.X-a.places.Home.X, pos.Z-a.places.Home.Z) < 5 {
		a.leading = false // brought them home
	}
	if a.checkHands(ctx) {
		return
	}
	// Game mode before anything that moves or remembers places: a lobby must not become home.
	every := 30 * time.Second
	if a.gameMode == "adventure" {
		every = 5 * time.Second // notice the move into the real world quickly
	}
	if time.Since(a.modeChecked) > every {
		a.modeChecked = time.Now()
		a.refreshGameMode(ctx)
	}
	if a.checkLobby(ctx) {
		return
	}
	if a.checkMarks(ctx) {
		return
	}
	a.writeMap()
	if a.checkWater(ctx) {
		return
	}
	if a.checkStuck(ctx, pos, busy) {
		return
	}
	if h := a.cfg.Home; h != nil && (a.places.Home == nil || a.places.Home.X != h.X || a.places.Home.Z != h.Z) {
		home := explore.Pos{X: h.X, Y: pos.Y, Z: h.Z} // set by hand (HOME=x,z): an open spot, not a bunker
		a.places.Home = &home
		a.homeSettled = false
		a.savePlaces()
		a.log.Info("home set from config", "x", explore.Block(h.X), "z", explore.Block(h.Z))
	}
	if a.places.Home == nil {
		home := pos
		a.places.Home = &home
		a.savePlaces()
		a.log.Info("home set", "x", explore.Block(pos.X), "z", explore.Block(pos.Z))
	}
	a.settleHome(ctx, pos)
	if !a.protected && a.cfg.ProtectRadius > 0 {
		a.protectBuilds(ctx)
	}
	// Stepped out from under the trees: take off again for the same target.
	if a.retryFlight != nil && time.Since(a.retrySince) > 40*time.Second {
		a.retryFlight = nil // never got out from under it: give up, it holds back everything else
	}
	if r := a.retryFlight; r != nil && !a.flying {
		// Out in the open, or the walk has ended. Right after the @goto the mod may not report the
		// walk yet: a take-off then fails under the same roof and burns the retry.
		// The open-sky column itself, not the roof next to it.
		if math.Hypot(pos.X-float64(a.retryFrom[0])-0.5, pos.Z-float64(a.retryFrom[1])-0.5) < 1.2 ||
			(!busy && time.Since(a.retrySince) > 6*time.Second) {
			a.retryFlight = nil
			a.flightFailedAt = time.Time{}
			if a.startFlightOpts(ctx, r[0], r[1], a.lastOpts) {
				return
			}
		}
	}
	if a.flying {
		if a.checkFlight(ctx) {
			return
		}
		if a.followAfter != "" { // flew to a player: now keep up with them on foot
			nick := a.followAfter
			a.followAfter = ""
			if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@follow " + nick}); err != nil {
				a.log.Warn("after flight follow", "err", err)
			}
			a.following = nick
		} else if x, z, ok := a.walkTarget(); ok { // landed near the target: walk the last metres
			if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", x, z)}); err != nil {
				a.log.Warn("after flight goto", "err", err)
			}
		}
	}
	if a.checkHover(ctx) {
		return
	}
	if !a.celebrateAt.IsZero() && time.Now().After(a.celebrateAt) && !a.flying {
		a.celebrateAt = time.Time{}
		a.celebrate(ctx, "landing")
	}
	if a.followAfter != "" && !a.flying && !a.hovering { // flew beside a player: keep up on foot
		nick := a.followAfter
		a.followAfter = ""
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@follow " + nick}); err != nil {
			a.log.Warn("after hover follow", "err", err)
		}
		a.following = nick
	}
	a.checkFollowAlive(ctx, busy)
	// Someone took off (the one we follow, or a player close by): fly beside them instead of
	// running below.
	if a.joinFlyers(ctx) {
		return
	}
	if a.outing != nil {
		a.checkOuting(ctx, pos)
		return
	}
	if a.activity != nil {
		a.checkActivity(ctx, pos)
		return
	}
	if a.maybeActivity(ctx) {
		return
	}
	mate := a.companion() // a companion who is not on the server is not followed
	if a.following != "" && mate == "" {
		return
	}
	if mate != "" {
		if a.following != "" || time.Now().Before(a.lingerUntil) {
			return
		}
		if time.Since(a.lastCommand) >= a.cfg.ExploreAfter {
			if a.trip != nil {
				a.cancelTrip(ctx)
			}
			a.log.Info("companion: staying close", "nick", mate)
			a.act(ctx, ActFollow, mate)
		}
		return
	}
	if !a.cfg.Explore {
		return
	}

	if a.trip == nil {
		if time.Since(a.lastCommand) < a.cfg.ExploreAfter {
			return
		}
		home := explore.ChunkOf(*a.places.Home)
		next, ok := explore.Next(home, explore.ChunkOf(pos), a.cfg.ExploreRadius, a.places.VisitedSet())
		if !ok {
			return // whole area explored
		}
		x, z := next.Center()
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", x, z)}); err != nil {
			a.log.Warn("explore goto", "err", err)
			return
		}
		now := time.Now()
		a.trip = &trip{target: next, started: now, lastPos: pos, lastMove: now}
		a.log.Info("explore: walking", "x", x, "z", z)
		return
	}

	t := a.trip
	x, z := t.target.Center()
	if math.Hypot(pos.X-float64(x), pos.Z-float64(z)) < arriveDist {
		a.survey(ctx, pos, t.target)
		a.trip = nil
		return
	}
	if math.Hypot(pos.X-t.lastPos.X, pos.Z-t.lastPos.Z) > 1 {
		t.lastPos, t.lastMove = pos, time.Now()
	}
	if time.Since(t.lastMove) > stuckAfter || time.Since(t.started) > tripTimeout {
		a.log.Info("explore: giving up on chunk", "x", x, "z", z)
		a.places.MarkVisited(t.target) // unreachable for now; do not retry forever
		a.savePlaces()
		a.cancelTrip(ctx)
	}
}

// checkFollowAlive re-sends @follow when the agent thinks it follows someone but the mod has
// stood idle for a while: a follow the mod dropped (a stop, a flight, a failed path) otherwise
// leaves the bot frozen, since roam does nothing while following.
func (a *Agent) checkFollowAlive(ctx context.Context, busy bool) {
	if a.following == "" || busy || a.flying {
		a.followIdleSince = time.Time{}
		return
	}
	if a.followIdleSince.IsZero() {
		a.followIdleSince = time.Now()
		return
	}
	if time.Since(a.followIdleSince) < 5*time.Second {
		return
	}
	a.followIdleSince = time.Time{}
	if _, _, _, _, ok := a.playerPos(ctx, a.following); !ok {
		a.log.Info("follow: player out of sight, stopping", "nick", a.following)
		a.following = ""
		return
	}
	a.log.Info("follow dropped by the mod, resuming", "nick", a.following)
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@follow " + a.following}); err != nil {
		a.log.Warn("resume follow", "err", err)
	}
}

// protectHome marks the area around home as no-break/no-place for the pathfinder. The zone
// lives in the client and is lost when the game restarts, so it is set on every start.
func (a *Agent) protectHome(ctx context.Context) {
	h := *a.places.Home
	// Zones accumulate across agent restarts while the game keeps running; start clean.
	if _, err := a.mc.Call(ctx, "clearProtectedAreas", nil); err != nil {
		a.log.Warn("clear protected areas", "err", err)
	}
	out, err := a.mc.Call(ctx, "markProtectedArea", map[string]any{
		"x": explore.Block(h.X), "y": explore.Block(h.Y), "z": explore.Block(h.Z), "r": a.cfg.ProtectRadius})
	if err != nil {
		a.log.Warn("protect area", "err", err)
		return
	}
	a.protected = true
	a.log.Info("protected area set", "result", out)
}

// survey looks at the surface around the bot and remembers anything notable.
func (a *Agent) survey(ctx context.Context, pos explore.Pos, target explore.Chunk) {
	a.places.MarkVisited(target)
	a.places.MarkVisited(explore.ChunkOf(pos))
	defer a.savePlaces()
	raw, err := a.mc.Call(ctx, "getSurfaceMap", map[string]any{"radius": 6})
	if err != nil {
		a.log.Warn("survey", "err", err)
		return
	}
	if err := a.places.AddSurface(pos, raw); err != nil {
		a.log.Warn("survey surface", "err", err)
	}
	f, err := explore.Summarize(raw)
	if err != nil {
		a.log.Warn("survey parse", "err", err)
		return
	}
	desc, ok := placeLabel(a.signsNear(ctx, 12), f)
	a.log.Info("explore: surveyed", "x", explore.Block(pos.X), "z", explore.Block(pos.Z), "built", f.Built, "water", f.Water, "landmark", desc)
	if !ok || !a.places.Add(explore.Place{Desc: desc, X: explore.Block(pos.X), Z: explore.Block(pos.Z), Seen: time.Now()}) {
		return
	}
	// Remember every find, but announce at most one per two minutes: on a busy server a walk
	// through a village would otherwise flood the chat.
	if time.Since(time.Unix(0, a.lastSpoke.Load())) < 2*time.Minute {
		return
	}
	a.lastSpoke.Store(time.Now().UnixNano())
	dir := explore.Direction(*a.places.Home, pos.X, pos.Z)
	a.enqueue(replyJob{
		prompt: fmt.Sprintf("Ты, гуляя, нашёл: %s, на x=%d z=%d, это на %s от твоей норы. "+
			"Коротко расскажи об этом в чат, в характере.", desc, explore.Block(pos.X), explore.Block(pos.Z), dir),
		at: time.Now(),
	})
}

func (a *Agent) cancelTrip(ctx context.Context) {
	a.trip = nil
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
		a.log.Warn("explore stop", "err", err)
	}
}

// homeSurfaceNear: how close the bot must be for the home column to be loaded and measured.
const homeSurfaceNear = 64.0

// settleHome puts the home point on the top surface of its column -- the ground, or the
// platform over it. Home.Y used to be the bot's own height when home was set, and once that was
// mid-air between the ground and a platform: flights home "arrived" up there and glided off.
// Measured once per run, when near enough for the column to be loaded.
func (a *Agent) settleHome(ctx context.Context, pos explore.Pos) {
	h := a.places.Home
	if h == nil || a.homeSettled || math.Hypot(h.X-pos.X, h.Z-pos.Z) > homeSurfaceNear {
		return
	}
	y, ok := a.surfaceY(ctx, explore.Block(h.X), explore.Block(h.Z), explore.Block(pos.Y)+64, explore.Block(pos.Y)-64)
	if !ok {
		return // not loaded yet: again next time
	}
	a.homeSettled = true
	if math.Abs(y-h.Y) >= 1 {
		a.log.Info("home put on the surface", "x", explore.Block(h.X), "z", explore.Block(h.Z), "from_y", h.Y, "y", y)
		h.Y = y
		a.savePlaces()
	}
}

// surfaceY is the height to stand at on the topmost solid block of column x,z between from and
// to (top down). Not ok when the column is not loaded or has nothing solid in that range.
func (a *Agent) surfaceY(ctx context.Context, x, z, from, to int) (float64, bool) {
	from, to = min(from, 319), max(to, -64)
	for y := from; y >= to; y-- {
		text, err := a.mc.Call(ctx, "getBlockAt", map[string]any{"x": x, "y": y, "z": z})
		if err != nil {
			return 0, false
		}
		var b struct {
			ID          string `json:"id"`
			Replaceable string `json:"replaceable"`
		}
		if json.Unmarshal([]byte(text), &b) != nil || b.ID == "minecraft:void_air" {
			return 0, false
		}
		if b.Replaceable == "false" {
			return float64(y + 1), true
		}
	}
	return 0, false
}

func (a *Agent) savePlaces() {
	if err := a.places.Save(); err != nil {
		a.log.Warn("save places", "err", err)
	}
}

// placesNote is what the reply prompt gets about known places.
func (a *Agent) placesNote() string {
	if !a.havePos || a.places.Home == nil {
		return ""
	}
	h := *a.places.Home
	note := fmt.Sprintf("- твоя нора (вход): x=%d y=%d z=%d, %d блоков на %s\n", explore.Block(h.X), explore.Block(h.Y), explore.Block(h.Z),
		int(math.Hypot(h.X-a.pos.X, h.Z-a.pos.Z)), explore.Direction(a.pos, h.X, h.Z))
	return note + a.places.Describe(a.pos, 5)
}

// flyOutMin: a flight out of a pit goes at least this far, so the take-off has room to climb.
const flyOutMin = 30

// flyOutTarget is where a stuck bot flies: the target it was walking to, pushed out to at least
// flyOutMin along the same line; without a target, a random point that far away.
func flyOutTarget(pos explore.Pos, tx, tz int, haveTarget bool) (int, int) {
	dx, dz := float64(tx)-pos.X, float64(tz)-pos.Z
	d := math.Hypot(dx, dz)
	if !haveTarget || d < 1 {
		a := rand.Float64() * 2 * math.Pi
		dx, dz, d = math.Cos(a), math.Sin(a), 1
	}
	if d >= flyOutMin {
		return tx, tz
	}
	k := flyOutMin / d
	return explore.Block(pos.X + dx*k), explore.Block(pos.Z + dz*k)
}
