package agent

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

// Walking on a public server, the way a player does it: look at the blocks around (the mod's
// getBlocksAround, radius 5, a tenth of a second), find on foot where one can actually get to,
// and take a short stride there -- towards the walk's target, or anywhere new when there is
// none. The next stride goes out before this one ends, so the bot keeps moving.
//
// Before this (SkyBars spawn, 21.11) every walk was one @goto to a chunk centre behind the spawn
// fences: Tungsten tried for 6-14 s, the stuck check gave up, the detour brought the bot back to
// the same spot, and it stood still most of the minute.

const (
	strideRadius   = 5                      // getBlocksAround caps the cube at 5
	strideNear     = 1.2                    // this close to the stride's cell: send the next one (sooner restarted the walk mid-way: jerky)
	strideIdle     = 250 * time.Millisecond // the mod idle this long after a stride: send the next
	strideMax      = 7 * time.Second        // a stride that takes longer has stalled
	strideFinal    = 4.5                    // the target is this close: walk straight to it
	strideNoGain   = 2                      // strides in a row that gain under a block: no way there on foot
	boxedInAfter   = 4                      // walks given up within boxedInWindow: wander what is reachable
	boxedInWindow  = 30 * time.Second
	boxedInWander  = 40 * time.Second
	visitCellShift = 1               // visits are counted per 2x2 blocks
	directOpen     = 3.5             // a stride gaining this much: open ground, walk straight to the target
	directStall    = 4 * time.Second // a straight walk without a block of progress this long: strides again
	directPause    = 10 * time.Second
)

type stride struct {
	to     [3]int
	goal   [2]int
	direct bool // straight to the target: the final approach, or open ground all the way
	at     time.Time
	noGain int
	// a direct walk's progress: stalled 4 s, it goes back to strides
	lastPos explore.Pos
	movedAt time.Time
}

// strideOn: walks go by strides now (public server, the fork answers getBlocksAround, on foot).
func (a *Agent) strideOn() bool {
	return a.cfg.PublicServer && a.strideFails < 3 && !a.flying && !a.hovering && a.following == ""
}

// noteVisit counts the 2x2 cells the bot has stood in, so strides prefer new ground.
func (a *Agent) noteVisit(pos explore.Pos) {
	cell := [2]int{explore.Block(pos.X) >> visitCellShift, explore.Block(pos.Z) >> visitCellShift}
	if cell == a.lastVisitCell && a.visits != nil {
		return
	}
	if a.visits == nil || len(a.visits) > 5000 {
		a.visits = map[[2]int]int{}
	}
	a.lastVisitCell = cell
	a.visits[cell]++
}

func (a *Agent) visitCount(x, z int) int {
	return min(a.visits[[2]int{x >> visitCellShift, z >> visitCellShift}], 3)
}

// driveStride keeps the current walk going by strides. It never holds back the rest of roam:
// arrival, activities and the stuck check run as before.
func (a *Agent) driveStride(ctx context.Context, pos explore.Pos, busy bool) {
	a.noteVisit(pos)
	if !a.strideOn() {
		a.stride = nil
		return
	}
	if a.reflexing() {
		return // the mod is fighting or fleeing: a walk now would cut across it
	}
	gx, gz, have := a.walkTarget()
	if a.activity != nil && a.activity.kind == "backtrack" {
		return // a precise step back the detour chose
	}
	wander := !have && time.Now().Before(a.wanderUntil)
	if !have && !wander {
		a.stride = nil
		return
	}
	goal := [2]int{gx, gz}
	s := a.stride
	if s != nil && s.goal == goal && s.direct {
		if math.Hypot(pos.X-s.lastPos.X, pos.Z-s.lastPos.Z) > 1 {
			s.lastPos, s.movedAt = pos, time.Now()
		}
		if (busy || time.Since(s.at) < 1500*time.Millisecond) && time.Since(s.movedAt) < directStall {
			return // walking straight to the target, one task, no restarts; arrival is roam's business
		}
		// Ended short of the target or stalled: strides again around whatever is in the way, and
		// no long walk for a while.
		a.noDirectUntil = time.Now().Add(directPause)
		a.stride, s = nil, nil
	}
	near := s != nil && math.Hypot(pos.X-float64(s.to[0])-0.5, pos.Z-float64(s.to[2])-0.5) < strideNear
	if s != nil && s.goal == goal && !near && (busy || time.Since(s.at) < strideIdle) && time.Since(s.at) < strideMax && s.noGain == 0 {
		return
	}
	if have && math.Hypot(pos.X-float64(gx), pos.Z-float64(gz)) < strideFinal {
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", gx, gz)}); err == nil {
			a.stride = &stride{goal: goal, direct: true, at: time.Now(), lastPos: pos, movedAt: time.Now()}
		}
		return
	}
	noGain := 0
	if s != nil && s.goal == goal {
		noGain = s.noGain
	}
	cell, gain, sent, long := a.stepToward(ctx, pos, gx, gz, have, time.Now().After(a.noDirectUntil))
	if long {
		a.stride = &stride{goal: goal, direct: true, at: time.Now(), lastPos: pos, movedAt: time.Now()}
		return
	}
	if !a.strideOn() {
		// No terrain from this mod (an older jar, the simulator): the old single @goto.
		a.log.Info("walk: no getBlocksAround here, walking by @goto")
		a.stride = nil
		if have {
			if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", gx, gz)}); err != nil {
				a.log.Warn("walk goto", "err", err)
			}
		}
		return
	}
	if have && (!sent || gain < 1) {
		noGain++
	} else {
		noGain = 0
	}
	if sent {
		a.stride = &stride{to: cell, goal: goal, at: time.Now(), noGain: noGain}
	} else if s != nil && s.goal == goal {
		s.noGain, s.at = noGain, time.Now()
	} else {
		a.stride = &stride{to: [3]int{explore.Block(pos.X), explore.Block(pos.Y), explore.Block(pos.Z)}, goal: goal, at: time.Now(), noGain: noGain}
	}
	if have && noGain >= strideNoGain {
		a.giveUpWalk(ctx, gx, gz)
	}
}

// stepToward sends one stride. gain is how far it gets along the way to the target. With long,
// open ground towards the target (the stride gains directOpen blocks or more) sends the whole
// walk to the target instead: one Tungsten task all the way, not a stop and a restart every few
// blocks (the jerky walking on SkyBars, 21.11). long reports that it did.
func (a *Agent) stepToward(ctx context.Context, pos explore.Pos, gx, gz int, have bool, longOK ...bool) ([3]int, float64, bool, bool) {
	raw, err := a.mc.Call(ctx, "getBlocksAround", map[string]any{"radius": strideRadius})
	center := [3]int{explore.Block(pos.X), explore.Block(pos.Y + 0.01), explore.Block(pos.Z)}
	terrain, ok := explore.ParseTerrain(raw, center, strideRadius)
	if err != nil || !ok || len(terrain.Blocks) == 0 {
		// Standing in a world, there is always some block within 5: none means no such tool.
		// Off for this world (resetModState turns it on again).
		a.strideFails = 3
		return [3]int{}, 0, false, false
	}
	a.strideFails = 0
	start := center
	for _, dy := range []int{0, 1, -1} { // on a slab or stairs the feet are in the block's cell
		if c := [3]int{center[0], center[1] + dy, center[2]}; terrain.Standable(c) {
			start = c
			break
		}
	}
	reach := terrain.Reachable(start, occupiedWalkCells(a.groundSnapshot, a.cfg.BotNick, pos))
	var hx, hz float64
	if have {
		hx, hz = float64(gx)-pos.X, float64(gz)-pos.Z
	}
	cell, gain, ok := explore.Stride(reach, start, hx, hz, a.visitCount)
	if !ok {
		return [3]int{}, 0, false, false
	}
	if have && len(longOK) > 0 && longOK[0] && gain >= directOpen {
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", gx, gz)}); err == nil {
			a.log.Debug("walk: open ground, straight to the target", "x", gx, "z", gz)
			return cell, gain, true, true
		}
	}
	cmd := fmt.Sprintf("@goto %d %d %d", cell[0], cell[1], cell[2])
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": cmd}); err != nil {
		return [3]int{}, 0, false, false
	}
	a.log.Debug("stride", "to", cell, "gain", math.Round(gain*10)/10, "reach", len(reach))
	return cell, gain, true, false
}

// giveUpWalk drops a target no stride gets closer to, at once instead of after the stuck check.
func (a *Agent) giveUpWalk(ctx context.Context, gx, gz int) {
	a.log.Info("explore: no way there on foot, next", "x", gx, "z", gz)
	a.stride = nil
	switch {
	case a.outing != nil:
		a.outing = nil
	case a.activity != nil:
		a.dropScoutTarget(a.activity)
		a.activity = nil
	case a.trip != nil:
		a.skipScoutChunk(a.trip.target)
		a.trip = nil
	case a.leading:
		a.leading = false
	case a.gameGoal != nil:
		return // a game's spot: the game decides
	}
	now := time.Now()
	kept := a.noWay[:0]
	for _, t := range a.noWay {
		if now.Sub(t) < boxedInWindow {
			kept = append(kept, t)
		}
	}
	a.noWay = append(kept, now)
	if len(a.noWay) >= boxedInAfter {
		a.noWay = nil
		a.wanderUntil = now.Add(boxedInWander)
		a.log.Info("explore: boxed in, wandering what is reachable")
	}
}
