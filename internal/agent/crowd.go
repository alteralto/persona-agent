package agent

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

// On a public server people are the point: the bot goes over to where they are and hangs about
// there, and scouts empty chunks only when nobody is around. Before this (SkyBars, 21.11) it
// walked chunk after chunk around its home whatever the players did, and walked off from a crowd
// as often as into one.

const (
	crowdRange  = 64.0            // blocks: players this far are worth walking over to
	crowdNear   = 6.0             // already this close: no walk needed
	crowdGroup  = 8.0             // players this close to each other are one group
	crowdAgain  = 3 * time.Minute // a group's chunk is not visited again sooner
	crowdLook   = 3 * time.Second // how often to look for a group to go to
	hangEvery   = 2 * time.Second // among people: look this often whether the last step is done
	hangStall   = 6 * time.Second // no block closer to the goal this long: the walk is stuck
	hangSkipFor = 30 * time.Second
	hangCloser  = 5.0             // further than this from the nearest player: step towards them
	hangRange   = 16.0            // nobody this close: nothing to hang about for
	crowdStop   = 3.0             // stop this far from the player walked to (visitShort's 12 left him aside)
	peopleLook  = 2 * time.Second // how long a look at who is near holds
	// hangAfterCommand: after being spoken to, a moment to answer before moving on among people.
	hangAfterCommand = 8 * time.Second
	hangMax          = 75 * time.Second // with one group at most this long...
	hangAreaSize     = 24.0             // ...within this many blocks of where it began
	leaveFor         = time.Minute      // then this long on his own: scouting or another group
)

// nearestPlayer is the distance to the closest other player, looked up at most every peopleLook.
func (a *Agent) nearestPlayer(ctx context.Context) float64 {
	if time.Since(a.peopleAt) < peopleLook {
		return a.peopleDist
	}
	a.peopleAt, a.peopleDist = time.Now(), math.Inf(1)
	for _, p := range a.visiblePlayers(ctx) {
		a.peopleDist = math.Min(a.peopleDist, p.dist)
	}
	return a.peopleDist
}

// amongPeople: on a public server with somebody within hangRange, the bot stays with them -- for
// hangMax at one spot, then moves on for leaveFor. A place that always has someone in it (the
// SkyBars casino, 21.11) otherwise held him for good.
func (a *Agent) amongPeople(ctx context.Context) bool {
	if !a.cfg.PublicServer || time.Now().Before(a.leaveUntil) || a.nearestPlayer(ctx) > hangRange {
		return false
	}
	if a.hangSince.IsZero() || math.Hypot(a.pos.X-a.hangAnchor.X, a.pos.Z-a.hangAnchor.Z) > hangAreaSize {
		a.hangSince, a.hangAnchor = time.Now(), a.pos
	}
	if time.Since(a.hangSince) > hangMax {
		a.leaveUntil, a.hangSince = time.Now().Add(leaveFor), time.Time{}
		if a.crowdSeen == nil {
			a.crowdSeen = map[explore.Chunk]time.Time{}
		}
		a.crowdSeen[explore.ChunkOf(a.pos)] = time.Now()
		a.log.Info("public: moving on from this group")
		return false
	}
	return true
}

// startCrowdVisit walks over to the biggest group of players in sight not visited lately.
func (a *Agent) startCrowdVisit(ctx context.Context) bool {
	if !a.cfg.PublicServer || !a.havePos || a.reflexing() || time.Since(a.lastCrowdLook) < crowdLook {
		return false
	}
	a.lastCrowdLook = time.Now()
	if a.crowdSeen == nil {
		a.crowdSeen = map[explore.Chunk]time.Time{}
	}
	if a.visited == nil { // the visit's arrival greets by these (checkActivity)
		a.visited, a.greeted = map[string]time.Time{}, map[string]time.Time{}
	}
	players := a.visiblePlayers(ctx)
	for _, p := range players {
		if p.dist < crowdNear {
			return false // already among people: hanging about and the usual walks do
		}
	}
	var best *seenPlayer
	bestGroup := -1
	for i := range players {
		p := players[i]
		if p.dist > crowdRange || p.dist < crowdNear {
			continue
		}
		if time.Since(a.crowdSeen[explore.ChunkOf(explore.Pos{X: p.x, Z: p.z})]) < crowdAgain {
			continue
		}
		group := 0
		for _, o := range players {
			if o.name != p.name && math.Hypot(o.x-p.x, o.z-p.z) < crowdGroup {
				group++
			}
		}
		if group > bestGroup || group == bestGroup && p.dist < best.dist {
			best, bestGroup = &players[i], group
		}
	}
	if best == nil {
		return false
	}
	k := (best.dist - crowdStop) / best.dist
	x := explore.Block(a.pos.X + (best.x-a.pos.X)*k)
	z := explore.Block(a.pos.Z + (best.z-a.pos.Z)*k)
	if !a.walkTo(ctx, x, z) {
		return false
	}
	a.crowdSeen[explore.ChunkOf(explore.Pos{X: best.x, Z: best.z})] = time.Now()
	a.partner, a.partnerAt = best.name, time.Now()
	a.activity = &activity{kind: "visit", x: x, z: z, label: best.name, started: time.Now()}
	a.log.Info("public: going over to people", "player", best.name, "with", bestGroup, "dist", int(best.dist))
	return true
}

// hangAround keeps the bot moving a little while it stays among people: a step towards the
// nearest one when it has drifted off, else a short stroll to a free spot nearby.
func (a *Agent) hangAround(ctx context.Context, pos explore.Pos, busy bool) {
	if !a.cfg.PublicServer || !a.strideOn() || a.judging() || a.reflexing() || time.Since(a.lastHang) < hangEvery {
		return
	}
	a.lastHang = time.Now()
	if busy {
		// Still walking to the last goal. Judged by progress towards it, not by any movement: on
		// SkyBars (21.11) a casino NPC stood in the way and the bot shuffled against it, 0.1-1
		// block a second back and forth, for 24 s, and "it moved" kept the walk alive.
		d := math.Hypot(pos.X-float64(a.hangGoal[0]), pos.Z-float64(a.hangGoal[1]))
		if a.hangGoalSet && d < a.hangBest-1 {
			a.hangBest, a.hangProgressAt = d, time.Now()
		}
		arrived := a.hangGoalSet && d < 1.5 // there, the mod just has not called it done yet: no stop
		if !arrived && (!a.hangGoalSet || time.Since(a.hangProgressAt) < hangStall) {
			return // under way: a new goal now restarts it from a standstill (jerky, 21.11)
		}
		if !arrived {
			if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err == nil {
				a.log.Info("public: dropped a stalled walk", "toward", a.hangWho)
			}
		}
		if !arrived && a.hangWho != "" {
			if a.hangSkip == nil {
				a.hangSkip = map[string]time.Time{}
			}
			a.hangSkip[strings.ToLower(a.hangWho)] = time.Now()
		}
	}
	a.hangGoalSet = false
	// Someone around, picked at random rather than the nearest: he drifts through the crowd from
	// one player to another instead of orbiting one (a step every 5 s, half of them skipped, read
	// as standing still on the SkyBars spawn, 21.11).
	var around []seenPlayer
	for _, p := range a.visiblePlayers(ctx) {
		if p.dist <= hangRange && time.Since(a.hangSkip[strings.ToLower(p.name)]) > hangSkipFor {
			around = append(around, p)
		}
	}
	if len(around) == 0 {
		return
	}
	p := around[rand.IntN(len(around))]
	if p.dist > hangCloser {
		// One walk to beside them, not strides of 3-5 blocks that each start from a standstill.
		k := (p.dist - 2) / p.dist
		x, z := explore.Block(pos.X+(p.x-pos.X)*k), explore.Block(pos.Z+(p.z-pos.Z)*k)
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", x, z)}); err == nil {
			a.hangGoal, a.hangGoalSet, a.hangWho = [2]int{x, z}, true, p.name
			a.hangBest, a.hangProgressAt = math.Hypot(pos.X-float64(x), pos.Z-float64(z)), time.Now()
		}
		return
	}
	// A stroll in a fresh random direction each time: the best "anywhere new" cell came out the same
	// few spots over and over, and from the side he looked stuck in a loop (21.11).
	angle := rand.Float64() * 2 * math.Pi
	rx, rz := explore.Block(pos.X+6*math.Cos(angle)), explore.Block(pos.Z+6*math.Sin(angle))
	if cell, _, sent, _ := a.stepToward(ctx, pos, rx, rz, true); sent {
		a.hangGoal, a.hangGoalSet, a.hangWho = [2]int{cell[0], cell[2]}, true, ""
		a.hangBest, a.hangProgressAt = math.Hypot(pos.X-float64(cell[0]), pos.Z-float64(cell[2])), time.Now()
	}
}
