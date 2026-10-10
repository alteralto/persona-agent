package agent

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

// outing is a walk the bot starts itself to a remembered place, inviting a companion along.
type outing struct {
	place   explore.Place
	started time.Time
}

const (
	outingTimeout = 2 * time.Minute
	outingLinger  = time.Minute // stay at the place before going back to the companion
)

// idleTurn is what the bot says after a quiet spell. It only talks: walks and flights come
// from the activity scheduler alone, so two timers never send the bot two ways at once. While an
// outing, activity or flight is under way the bot already speaks about it.
func (a *Agent) idleTurn(ctx context.Context) {
	if a.outing != nil || a.activity != nil || a.flying || !a.havePos {
		return // nobody to talk to in the main menu
	}
	a.enqueue(replyJob{prompt: a.idlePrompt(), history: slices.Clone(a.history), at: time.Now()})
}

func (a *Agent) startOuting(ctx context.Context) bool {
	if a.places == nil || len(a.places.List) == 0 || !a.havePos || len(a.nearbyBots()) == 0 {
		return false
	}
	// Pick a place not too close (no point) and not too far (they will not follow).
	var options []explore.Place
	for _, p := range a.places.List {
		d := math.Hypot(float64(p.X)-a.pos.X, float64(p.Z)-a.pos.Z)
		if d > 12 && d < 120 {
			options = append(options, p)
		}
	}
	if len(options) == 0 {
		return false
	}
	p := options[rand.IntN(len(options))]
	far := math.Hypot(float64(p.X)-a.pos.X, float64(p.Z)-a.pos.Z) > a.flyAbove()
	if !(far && a.startFlight(ctx, p.X, p.Z)) {
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", p.X, p.Z)}); err != nil {
			a.log.Warn("outing goto", "err", err)
			return false
		}
	}
	a.trip, a.following = nil, ""
	a.outing = &outing{place: p, started: time.Now()}
	bots := a.nearbyBots()
	who := spokenName(bots[rand.IntN(len(bots))])
	dist := int(math.Hypot(float64(p.X)-a.pos.X, float64(p.Z)-a.pos.Z))
	dir := explore.Direction(a.pos, float64(p.X), float64(p.Z))
	a.log.Info("outing: leading", "to", p.Desc, "x", p.X, "z", p.Z, "invite", who)
	a.enqueue(replyJob{
		prompt: fmt.Sprintf("Ситуация: ты сам решил прогуляться и уже идёшь к месту, которое нашёл раньше: %s, "+
			"%d блоков на %s. Твой знакомый %s стоит рядом. Напиши реплику, в которой ты по имени зовёшь его с собой. "+
			"Пример: «%s, айда со мной к голубому домику, тут недалеко!» Одна фраза до 90 символов, без координат.",
			p.Desc, dist, dir, who, who),
		history: slices.Clone(a.history),
		at:      time.Now(),
	})
	return true
}

// checkOuting runs instead of the usual roaming while an outing is under way.
func (a *Agent) checkOuting(ctx context.Context, pos explore.Pos) {
	o := a.outing
	if math.Hypot(pos.X-float64(o.place.X), pos.Z-float64(o.place.Z)) < 5 {
		a.log.Info("outing: arrived", "at", o.place.Desc)
		a.outing = nil
		a.lingerFor(outingLinger) // stay at the place a while before following again
		return
	}
	limit := outingTimeout
	if a.flying {
		limit += time.Minute // flights to far places take longer than walks
	}
	if time.Since(o.started) > limit {
		a.log.Info("outing: gave up", "at", o.place.Desc)
		a.outing = nil
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
			a.log.Warn("outing stop", "err", err)
		}
	}
}

// spokenName turns a nickname into the name the character uses for that player.
func spokenName(nick string) string {
	switch strings.ToLower(nick) {
	case "pon4ikyt", "pon4ik":
		return "Пончик"
	case "nettyan":
		return "Нетян"
	}
	return nick
}

// flyShowOff answers "let's fly!": take off towards a known place 60–150 blocks away, or
// straight ahead 80 blocks when nothing is known yet.
// flyAbove is the distance beyond which the bot flies instead of walking. In creative the
// elytra and rockets are free and walking through a village with no digging and no placing
// gets stuck, so it flies almost everywhere.
func (a *Agent) flyAbove() float64 {
	if a.creative && a.fork {
		return 40 // shorter hops overshoot: the take-off rocket alone carries 30-40 blocks
	}
	return 60
}

func (a *Agent) flyShowOff(ctx context.Context, low bool) bool {
	if !a.havePos {
		return false
	}
	// No remembered place far enough: out from home in any direction, or back home when already
	// far, so a string of flight shows does not carry the bot away from its base.
	cx, cz := a.pos.X, a.pos.Z
	if a.places != nil && a.places.Home != nil {
		cx, cz = a.places.Home.X, a.places.Home.Z
	}
	angle := rand.Float64() * 2 * math.Pi
	x, z := int(cx+80*math.Cos(angle)), int(cz+80*math.Sin(angle))
	if math.Hypot(a.pos.X-cx, a.pos.Z-cz) > 60 {
		x, z = int(cx), int(cz)
	}
	// Flight testing (FLIGHT_SHARE): long legs, out 120-350 blocks from home and back home.
	if a.cfg.FlightShare > 0 && a.places != nil && a.places.Home != nil {
		// Strictly out, home, out, home: by distance alone the bot, exploring after each landing,
		// was always "far from home" and flew nothing but home legs.
		home := a.testLegOut && math.Hypot(a.pos.X-cx, a.pos.Z-cz) > 40
		if home {
			x, z = int(cx), int(cz)
		} else {
			r := 120 + rand.Float64()*230
			x, z = int(cx+r*math.Cos(angle)), int(cz+r*math.Sin(angle))
		}
		a.testLegOut = !home
		a.log.Info("flight test leg", "x", x, "z", z, "home", home, "low", low)
		return a.startFlightLow(ctx, x, z, low)
	}
	if a.places != nil {
		var far []explore.Place
		for _, p := range a.places.List {
			if d := math.Hypot(float64(p.X)-a.pos.X, float64(p.Z)-a.pos.Z); d > 60 && d < 200 {
				far = append(far, p)
			}
		}
		if len(far) > 0 {
			p := far[rand.IntN(len(far))]
			x, z = p.X, p.Z
		}
	}
	return a.startFlightLow(ctx, x, z, low)
}
