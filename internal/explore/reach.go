package explore

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Terrain is the mod's getBlocksAround: every non-air block in a cube around the bot, by block
// position. Cells inside the cube that are missing are air; cells outside it are unknown.
type Terrain struct {
	Blocks map[[3]int]string // lower-case block names, as the client's language prints them
	Center [3]int            // the bot's block when the cube was taken
	Radius int
}

// ParseTerrain reads getBlocksAround ([{position:"x, y, z", block, hardness}]).
func ParseTerrain(raw string, center [3]int, radius int) (Terrain, bool) {
	var blocks []struct{ Position, Block string }
	if json.Unmarshal([]byte(raw), &blocks) != nil {
		return Terrain{}, false
	}
	t := Terrain{Blocks: make(map[[3]int]string, len(blocks)), Center: center, Radius: radius}
	for _, b := range blocks {
		parts := strings.Split(b.Position, ",")
		if len(parts) != 3 || b.Block == "" {
			continue
		}
		var cell [3]int
		ok := true
		for i, s := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				ok = false
				break
			}
			cell[i] = n
		}
		if ok {
			t.Blocks[cell] = strings.ToLower(b.Block)
		}
	}
	return t, true
}

// Block names are matched by fragments, in Russian (the client's language) and English.
var (
	// No collision: the feet and the head pass through.
	passWords = []string{
		"трава", "папоротник", "цвет", "одуванч", "мак", "василёк", "тюльпан", "ромашк", "орхидея",
		"аллиум", "лилия", "росток", "саженец", "факел", "табличк", "кнопк", "рычаг", "нажимная плита",
		"рельс", "ковёр", "ковер", "лиана", "лоза", "плющ", "мёртвый куст", "сухой куст", "тростник",
		"пшеница", "морковь", "картофель", "свёкла", "редстоуновая пыль", "редстоун-пыль", "флаг",
		"блок света", "воздух",
		"short grass", "tall grass", "fern", "flower", "dandelion", "poppy", "tulip", "torch", "sign",
		"button", "lever", "pressure plate", "rail", "carpet", "vine", "sapling", "dead bush",
		"sugar cane", "wheat", "carrots", "potatoes", "beetroots", "redstone wire", "banner",
		"light block", "structure void",
	}
	// Never step in or onto.
	hazardWords = []string{
		"лава", "огонь", "магм", "кактус", "портал", "костёр", "костер", "ягод", "порошковый снег",
		"паутин", "вода", "пузыр",
		"lava", "fire", "magma", "cactus", "portal", "campfire", "berry", "powder snow", "cobweb",
		"water", "bubble",
	}
	// Taller than a jump: fences, walls, gates, bars, panes.
	tallWords = []string{
		"забор", "калитк", "стена", "решётк", "решетк", "панель", "цепь", "дверь",
		"fence", "wall", "gate", "bars", "pane", "chain", "door",
	}
)

func hasAny(name string, words []string) bool {
	for _, w := range words {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

func (t Terrain) known(c [3]int) bool {
	return abs(c[0]-t.Center[0]) <= t.Radius && abs(c[1]-t.Center[1]) <= t.Radius && abs(c[2]-t.Center[2]) <= t.Radius
}

// open: the body passes through the cell (air or a plant, a torch, a sign...).
func (t Terrain) open(c [3]int) bool {
	if !t.known(c) {
		return false
	}
	name, ok := t.Blocks[c]
	if !ok {
		return true
	}
	if name == "снег" || name == "snow" { // a snow layer, not a snow block
		return true
	}
	return hasAny(name, passWords) && !hasAny(name, hazardWords)
}

// floor: a block one can stand on (full, slab, stairs, glass, leaves...).
func (t Terrain) floor(c [3]int) bool {
	if !t.known(c) {
		return false
	}
	name, ok := t.Blocks[c]
	if !ok || name == "снег" || name == "snow" {
		return false
	}
	return !hasAny(name, passWords) && !hasAny(name, hazardWords) && !hasAny(name, tallWords)
}

// Standable: the feet and the head fit and there is a floor below.
func (t Terrain) Standable(c [3]int) bool {
	return t.open(c) && t.open([3]int{c[0], c[1] + 1, c[2]}) && t.floor([3]int{c[0], c[1] - 1, c[2]})
}

// Reachable walks the terrain from start on foot: the four sides, a step or a jump up one
// block, a drop of up to three. It returns the walking distance in steps to every cell reached.
// occupied cells (players standing there) are not entered.
func (t Terrain) Reachable(start [3]int, occupied map[[2]int]bool) map[[3]int]int {
	dist := map[[3]int]int{start: 0}
	queue := [][3]int{start}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			x, z := c[0]+d[0], c[2]+d[1]
			if occupied[[2]int{x, z}] {
				continue
			}
			for _, dy := range []int{0, 1, -1, -2, -3} {
				n := [3]int{x, c[1] + dy, z}
				if _, seen := dist[n]; seen || !t.Standable(n) {
					continue
				}
				if dy == 1 && !t.open([3]int{c[0], c[1] + 2, c[2]}) {
					continue // no room to jump
				}
				if dy < 0 { // the column it falls down must be clear
					clear := true
					for y := c[1] + 1; y > n[1]+1; y-- {
						if !t.open([3]int{x, y, z}) {
							clear = false
							break
						}
					}
					if !clear {
						continue
					}
				}
				dist[n] = dist[c] + 1
				queue = append(queue, n)
				break // one level per side: the first that works, flat before up before down
			}
		}
	}
	return dist
}

// Stride picks the next short walk: a reachable cell a few blocks off, as far along the heading
// (hx, hz; zero for none) as the ground allows, away from cells already walked (visits).
// progress is how many blocks the cell gains along the heading.
func Stride(reach map[[3]int]int, start [3]int, hx, hz float64, visits func(x, z int) int) (cell [3]int, progress float64, ok bool) {
	if l := math.Hypot(hx, hz); l > 0 {
		hx, hz = hx/l, hz/l
	}
	best := math.Inf(-1)
	for c, steps := range reach {
		dx, dz := float64(c[0]-start[0]), float64(c[2]-start[2])
		flat := math.Hypot(dx, dz)
		if steps < 2 || flat < 2.5 {
			continue
		}
		along := dx*hx + dz*hz
		score := along + 0.25*math.Min(flat, 5) - 0.15*(float64(steps)-flat)
		if visits != nil {
			score -= 1.5 * float64(visits(c[0], c[2]))
		}
		if score > best || score == best && (c[0] < cell[0] || c[0] == cell[0] && c[2] < cell[2]) {
			best, cell, progress, ok = score, c, along, true
		}
	}
	return cell, progress, ok
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
