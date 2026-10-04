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
	var st struct {
		Pos  string `json:"pos"`
		Busy bool   `json:"busy"`
	}
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		return explore.Pos{}, false, err
	}
	pos, err := explore.ParsePos(st.Pos)
	return pos, st.Busy, err
}

const stuckFor = 10 * time.Second

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
		return int(a.places.Home.X), int(a.places.Home.Z), true
	}
	return 0, 0, false
}

// checkStuck notices a walk that makes no progress: busy, yet the bot has not moved.
// It stops the walk and hops a few blocks aside, out of the corner or pit.
func (a *Agent) checkStuck(ctx context.Context, pos explore.Pos, busy bool) bool {
	// Following keeps the mod "busy" even while standing next to the target, so only walks to
	// a point (exploration, outing, leading home) are checked.
	walkingToPoint := a.trip != nil || a.outing != nil || a.leading || a.activity != nil
	if !busy || !walkingToPoint || math.Hypot(pos.X-a.stillPos.X, pos.Z-a.stillPos.Z) > 1 || math.Abs(pos.Y-a.stillPos.Y) > 1 {
		a.stillPos, a.stillSince = pos, time.Now()
		return false
	}
	if time.Since(a.stillSince) < stuckFor {
		return false
	}
	// In creative, walls and fences we may not break are simply flown over.
	if tx, tz, ok := a.walkTarget(); ok && a.creative && a.startFlight(ctx, tx, tz) {
		a.log.Info("stuck: flying over instead", "x", int(pos.X), "z", int(pos.Z), "to_x", tx, "to_z", tz)
		a.trip, a.leading = nil, false // the outing/activity keeps its target; flight lands near it
		a.stillPos, a.stillSince = pos, time.Now()
		return true
	}
	angle := rand.Float64() * 2 * math.Pi
	dist := 6 + rand.Float64()*4
	x, z := int(pos.X+dist*math.Cos(angle)), int(pos.Z+dist*math.Sin(angle))
	a.log.Info("stuck: hopping aside", "x", int(pos.X), "z", int(pos.Z), "to_x", x, "to_z", z)
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
		a.log.Warn("position", "err", err)
		return
	}
	a.pos, a.havePos = pos, true
	if a.leading && a.places.Home != nil && math.Hypot(pos.X-a.places.Home.X, pos.Z-a.places.Home.Z) < 5 {
		a.leading = false // brought them home
	}
	if a.checkStuck(ctx, pos, busy) {
		return
	}
	if a.places.Home == nil {
		home := pos
		a.places.Home = &home
		a.savePlaces()
		a.log.Info("home set", "x", int(pos.X), "z", int(pos.Z))
	}
	if !a.protected && a.cfg.ProtectRadius > 0 {
		a.protectBuilds(ctx)
	}
	if time.Since(a.modeChecked) > 30*time.Second {
		a.modeChecked = time.Now()
		a.refreshGameMode(ctx)
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
	a.checkFollowAlive(ctx, busy)
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
		"x": int(h.X), "y": int(h.Y), "z": int(h.Z), "r": a.cfg.ProtectRadius})
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
	f, err := explore.Summarize(raw)
	if err != nil {
		a.log.Warn("survey parse", "err", err)
		return
	}
	desc, ok := placeLabel(a.signsNear(ctx, 12), f)
	a.log.Info("explore: surveyed", "x", int(pos.X), "z", int(pos.Z), "built", f.Built, "water", f.Water, "landmark", desc)
	if !ok || !a.places.Add(explore.Place{Desc: desc, X: int(pos.X), Z: int(pos.Z), Seen: time.Now()}) {
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
			"Коротко расскажи об этом в чат, в характере.", desc, int(pos.X), int(pos.Z), dir),
		at: time.Now(),
	})
}

func (a *Agent) cancelTrip(ctx context.Context) {
	a.trip = nil
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
		a.log.Warn("explore stop", "err", err)
	}
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
	note := fmt.Sprintf("- твоя нора (вход): x=%d y=%d z=%d, %d блоков на %s\n", int(h.X), int(h.Y), int(h.Z),
		int(math.Hypot(h.X-a.pos.X, h.Z-a.pos.Z)), explore.Direction(a.pos, h.X, h.Z))
	return note + a.places.Describe(a.pos, 5)
}
