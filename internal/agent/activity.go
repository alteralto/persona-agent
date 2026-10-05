package agent

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

// activity is something the bot does on its own initiative, independent of how chatty the
// others are: idle-only behaviour never fired while two other bots talked non-stop.
type activity struct {
	kind    string // "sign" | "scout"
	x, z    int
	label   string
	started time.Time
}

const activityTimeout = 90 * time.Second

// maybeActivity starts an activity when nobody has asked the bot for anything lately.
func (a *Agent) maybeActivity(ctx context.Context) bool {
	if a.cfg.ActivityEvery <= 0 || a.outing != nil || a.activity != nil || a.flying || a.trip != nil || !a.havePos {
		return false
	}
	// "Стой тут" holds the bot for a minute: no flight shows in the meantime.
	if time.Since(a.lastActivity) < a.cfg.ActivityEvery || time.Since(a.lastRequest) < 30*time.Second ||
		time.Now().Before(a.lingerUntil) {
		return false
	}
	a.lastActivity = time.Now()
	// FlightShare > 0 (flight testing): that share of activities are flights, the rest as usual.
	if a.cfg.FlightShare > 0 && rand.Float64() < a.cfg.FlightShare && a.startFlightShow(ctx) {
		return true
	}
	r := rand.Float64()
	switch {
	case r < 0.35 && a.startOuting(ctx):
		return true
	case r >= 0.35 && r < 0.65 && a.startSignTour(ctx):
		return true
	case r >= 0.65 && r < 0.85 && a.startScout(ctx):
		return true
	case r >= 0.85 && a.startFlightShow(ctx):
		return true
	}
	// The drawn one was not possible (no signs left, no places yet): try the others.
	return a.startSignTour(ctx) || a.startOuting(ctx) || a.startScout(ctx)
}

// startSignTour walks to the nearest sign not read yet.
func (a *Agent) startSignTour(ctx context.Context) bool {
	var best *sign
	for _, s := range a.signsNear(ctx, 48) {
		s := s
		text := signText(s)
		if text == "" || s.Distance < 4 || a.readSigns[s.Pos] {
			continue
		}
		best = &s
		break // nearest first
	}
	if best == nil {
		return false
	}
	x, z, ok := parseBlockPos(best.Pos)
	if !ok {
		return false
	}
	if !a.walkTo(ctx, x, z) {
		return false
	}
	a.activity = &activity{kind: "sign", x: x, z: z, label: signText(*best), started: time.Now()}
	a.readSigns[best.Pos] = true
	a.log.Info("activity: reading a sign", "text", a.activity.label, "x", x, "z", z)
	return true
}

// startScout walks to an unvisited chunk next to the bot, to find new places and signs.
func (a *Agent) startScout(ctx context.Context) bool {
	cur := explore.ChunkOf(a.pos)
	// Around home, not around wherever the bot is: scouting "next door" each time walked it
	// ever further away -- into a lake, last time.
	center, radius := cur, 2
	if a.places.Home != nil {
		center, radius = explore.ChunkOf(*a.places.Home), max(a.cfg.ExploreRadius, 4)
	}
	next, ok := explore.Next(center, cur, radius, a.places.VisitedSet())
	if !ok {
		return false
	}
	x, z := next.Center()
	if !a.walkTo(ctx, x, z) {
		return false
	}
	a.activity = &activity{kind: "scout", x: x, z: z, started: time.Now()}
	a.log.Info("activity: scouting", "x", x, "z", z)
	return true
}

// startFlightShow takes off for a far known place and says so.
func (a *Agent) startFlightShow(ctx context.Context) bool {
	if !a.flyShowOff(ctx) {
		return false
	}
	a.log.Info("activity: flight")
	a.enqueue(replyJob{
		prompt: "Ты сам решил полетать: надеваешь элитры и взлетаешь на ракетах, хоть и боишься высоты. " +
			"Скажи об этом одной короткой фразой до 90 символов, в характере.",
		at: time.Now(),
	})
	return true
}

// checkActivity runs while an activity is under way.
func (a *Agent) checkActivity(ctx context.Context, pos explore.Pos) {
	act := a.activity
	if math.Hypot(pos.X-float64(act.x), pos.Z-float64(act.z)) < 4.5 {
		a.activity = nil
		a.lingerFor(20 * time.Second) // a moment at the spot before drifting back
		switch act.kind {
		case "sign":
			a.places.Add(explore.Place{Desc: "табличка «" + act.label + "»", X: act.x, Z: act.z, Seen: time.Now()})
			a.savePlaces()
			a.enqueue(replyJob{
				prompt: fmt.Sprintf("Ты подошёл к табличке, на ней написано: «%s». Прочитай её вслух своими "+
					"словами и коротко прокомментируй, в характере. Одна-две фразы до 120 символов.", act.label),
				history: append([]string(nil), a.history...),
				at:      time.Now(),
			})
		case "scout":
			a.survey(ctx, pos, explore.ChunkOf(pos))
		}
		return
	}
	if time.Since(act.started) > activityTimeout {
		a.log.Info("activity: gave up", "kind", act.kind)
		a.activity = nil
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
			a.log.Warn("activity stop", "err", err)
		}
	}
}

func (a *Agent) walkTo(ctx context.Context, x, z int) bool {
	a.following = ""
	if math.Hypot(float64(x)-a.pos.X, float64(z)-a.pos.Z) > a.flyAbove() && a.startFlight(ctx, x, z) {
		return true
	}
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", x, z)}); err != nil {
		a.log.Warn("activity goto", "err", err)
		return false
	}
	return true
}

func signText(s sign) string {
	t := strings.TrimSpace(s.Front)
	if t == "" {
		t = strings.TrimSpace(s.Back)
	}
	return t
}

// parseBlockPos reads "x,y,z" as printed by readSigns.
func parseBlockPos(s string) (int, int, bool) {
	p := strings.Split(s, ",")
	if len(p) != 3 {
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(strings.TrimSpace(p[0]))
	z, err2 := strconv.Atoi(strings.TrimSpace(p[2]))
	return x, z, err1 == nil && err2 == nil
}
