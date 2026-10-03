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

// idleTurn is what the bot does after a quiet spell: every other time it leads an outing to a
// known place, otherwise it just talks to someone nearby.
func (a *Agent) idleTurn(ctx context.Context) {
	a.idleTurns++
	if a.idleTurns%2 == 1 && a.startOuting(ctx) {
		return
	}
	a.enqueue(replyJob{prompt: a.idlePrompt(), history: slices.Clone(a.history), at: time.Now()})
}

func (a *Agent) startOuting(ctx context.Context) bool {
	if a.places == nil || len(a.places.List) == 0 || !a.havePos || len(a.cfg.OtherBots) == 0 {
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
	far := math.Hypot(float64(p.X)-a.pos.X, float64(p.Z)-a.pos.Z) > 60
	if !(far && a.startFlight(ctx, p.X, p.Z)) {
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", p.X, p.Z)}); err != nil {
			a.log.Warn("outing goto", "err", err)
			return false
		}
	}
	a.trip, a.following = nil, ""
	a.outing = &outing{place: p, started: time.Now()}
	who := spokenName(a.cfg.OtherBots[rand.IntN(len(a.cfg.OtherBots))])
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
		a.lastCommand = time.Now().Add(outingLinger - a.cfg.ExploreAfter) // linger before following again
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
func (a *Agent) flyShowOff(ctx context.Context) bool {
	if !a.havePos {
		return false
	}
	x, z := int(a.pos.X)+80, int(a.pos.Z)
	if a.places != nil {
		var far []explore.Place
		for _, p := range a.places.List {
			if d := math.Hypot(float64(p.X)-a.pos.X, float64(p.Z)-a.pos.Z); d > 60 && d < 150 {
				far = append(far, p)
			}
		}
		if len(far) > 0 {
			p := far[rand.IntN(len(far))]
			x, z = p.X, p.Z
		}
	}
	return a.startFlight(ctx, x, z)
}
