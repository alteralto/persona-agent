package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

// getBlocksAround omits air and reports translated block names in older jars.
// Accept only known full walking surfaces. Unknown terrain stays with Tungsten.
var groundBlocks = map[string]bool{
	"stone": true, "камень": true, "stone bricks": true, "каменные кирпичи": true,
	"mossy stone bricks": true, "замшелые каменные кирпичи": true,
	"cracked stone bricks": true, "потрескавшиеся каменные кирпичи": true,
	"cobblestone": true, "булыжник": true, "smooth stone": true, "гладкий камень": true,
	"dirt": true, "земля": true, "grass block": true, "дёрн": true,
}

func occupiedWalkCells(snapshot, self string, pos explore.Pos) map[[2]int]bool {
	var state struct{ Players []struct{ Name, Pos string } }
	if snapshot == "" || json.Unmarshal([]byte(snapshot), &state) != nil {
		return nil
	}
	cells := make(map[[2]int]bool)
	for _, player := range state.Players {
		if strings.EqualFold(player.Name, self) {
			continue
		}
		p, err := explore.ParsePos(player.Pos)
		if err != nil || !(math.Abs(p.Y-pos.Y) < 1.8 && math.Abs(p.X-pos.X) <= 5 && math.Abs(p.Z-pos.Z) <= 5) {
			continue
		}
		// A standing player is 0.6 blocks wide and can straddle a cell edge.
		for x := explore.Block(p.X - 0.3); x <= explore.Block(p.X+0.3); x++ {
			for z := explore.Block(p.Z - 0.3); z <= explore.Block(p.Z+0.3); z++ {
				cells[[2]int{x, z}] = true
			}
		}
	}
	return cells
}

func levelDetour(raw string, pos explore.Pos, tx, tz int, occupied map[[2]int]bool) ([3]int, bool) {
	var blocks []struct{ Position, Block string }
	if json.Unmarshal([]byte(raw), &blocks) != nil {
		return [3]int{}, false
	}
	terrain := make(map[[3]int]string, len(blocks))
	for _, b := range blocks {
		parts := strings.Split(b.Position, ",")
		if len(parts) != 3 || b.Block == "" {
			return [3]int{}, false
		}
		var cell [3]int
		for i, s := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return [3]int{}, false
			}
			cell[i] = n
		}
		terrain[cell] = strings.ToLower(b.Block)
	}
	start := [3]int{explore.Block(pos.X), explore.Block(pos.Y), explore.Block(pos.Z)}
	if math.Abs(pos.Y-float64(start[1])) > 0.12 {
		return [3]int{}, false
	}
	var best [3]int
	bestDistance := math.Inf(1)
	for _, dir := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		for step := 1; step <= 4; step++ {
			x, y, z := start[0]+dir[0]*step, start[1], start[2]+dir[1]*step
			if occupied[[2]int{x, z}] || !groundBlocks[terrain[[3]int{x, y - 1, z}]] || terrain[[3]int{x, y, z}] != "" || terrain[[3]int{x, y + 1, z}] != "" {
				break
			}
			if step < 3 {
				continue
			}
			d := math.Hypot(float64(tx-x), float64(tz-z))
			if d < bestDistance {
				best, bestDistance = [3]int{x, y, z}, d
			}
		}
	}
	return best, !math.IsInf(bestDistance, 1)
}

func (a *Agent) tryLevelDetour(ctx context.Context, pos explore.Pos, tx, tz int) bool {
	raw, err := a.mc.Call(ctx, "getBlocksAround", map[string]any{"radius": 4})
	if err != nil {
		return false
	}
	p, ok := levelDetour(raw, pos, tx, tz, occupiedWalkCells(a.groundSnapshot, a.cfg.BotNick, pos))
	if !ok {
		return false
	}
	cmd := fmt.Sprintf("@goto %d %d %d", p[0], p[1], p[2])
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": cmd}); err != nil {
		return false
	}
	a.activity = &activity{kind: "backtrack", x: p[0], z: p[2], started: time.Now()}
	a.log.Info("explore: taking clear ground detour", "x", p[0], "y", p[1], "z", p[2])
	return true
}
