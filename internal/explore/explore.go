// Package explore walks the bot chunk by chunk around its home and remembers what it saw.
package explore

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type Pos struct{ X, Y, Z float64 }

type Chunk struct{ X, Z int }

func ChunkOf(p Pos) Chunk {
	return Chunk{int(math.Floor(p.X)) >> 4, int(math.Floor(p.Z)) >> 4}
}

// Center returns the block x,z in the middle of the chunk.
func (c Chunk) Center() (int, int) { return c.X*16 + 8, c.Z*16 + 8 }

// ParsePos reads pathStatus "pos". The mod formats numbers with the client locale, so on a
// Russian client "-435,6,70,0,-134,2" means -435.6, 70.0, -134.2.
func ParsePos(s string) (Pos, error) {
	parts := strings.Split(strings.ReplaceAll(s, " ", ""), ",")
	var nums []string
	switch len(parts) {
	case 3:
		nums = parts
	case 6:
		nums = []string{parts[0] + "." + parts[1], parts[2] + "." + parts[3], parts[4] + "." + parts[5]}
	default:
		return Pos{}, fmt.Errorf("unexpected pos %q", s)
	}
	var v [3]float64
	for i, n := range nums {
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return Pos{}, fmt.Errorf("pos %q: %w", s, err)
		}
		v[i] = f
	}
	return Pos{v[0], v[1], v[2]}, nil
}

// Next picks the unvisited chunk within radius (in chunks) of home that is closest to cur.
// ok=false when the whole area has been visited.
func Next(home, cur Chunk, radius int, visited map[Chunk]bool) (Chunk, bool) {
	best, bestD, found := Chunk{}, math.MaxFloat64, false
	for dx := -radius; dx <= radius; dx++ {
		for dz := -radius; dz <= radius; dz++ {
			c := Chunk{home.X + dx, home.Z + dz}
			if visited[c] {
				continue
			}
			// Prefer near the bot, break ties toward home so the walk spirals outward.
			d := math.Hypot(float64(c.X-cur.X), float64(c.Z-cur.Z)) + 0.01*math.Hypot(float64(dx), float64(dz))
			if d < bestD {
				best, bestD, found = c, d, true
			}
		}
	}
	return best, found
}

// Column is one entry of getSurfaceMap.
type Column struct {
	DX    string `json:"dx"`
	DZ    string `json:"dz"`
	Block string `json:"block"`
}

// Finding summarizes what stands out in a surface map.
type Finding struct {
	Built   int            // columns topped by man-made blocks
	Water   int            // columns of water
	Colors  map[string]int // colored building blocks by color word, e.g. "розов"
	Notable []string       // distinct man-made block names, up to a few
}

var builtWords = []string{"доск", "стекл", "шерст", "бетон", "терракот", "кирпич", "табличк", "забор",
	"дверь", "ступен", "плит", "фонар", "кварц", "булыжник", "каменные кирпичи", "ковёр", "кровать"}

var colorWords = []string{"розов", "красн", "оранж", "жёлт", "лайм", "зелён", "голуб", "син", "фиолет",
	"пурпур", "бел", "чёрн", "сер", "коричн", "бирюз"}

// Summarize classifies surface blocks by their (Russian) names.
func Summarize(raw string) (Finding, error) {
	var cols []Column
	if err := json.Unmarshal([]byte(raw), &cols); err != nil {
		return Finding{}, err
	}
	f := Finding{Colors: map[string]int{}}
	seen := map[string]bool{}
	for _, c := range cols {
		name := strings.ToLower(c.Block)
		if strings.Contains(name, "вода") {
			f.Water++
			continue
		}
		if !containsAny(name, builtWords) {
			continue
		}
		f.Built++
		for _, w := range colorWords {
			if strings.Contains(name, w) {
				f.Colors[w]++
				break
			}
		}
		if !seen[name] && len(f.Notable) < 5 {
			seen[name] = true
			f.Notable = append(f.Notable, c.Block)
		}
	}
	return f, nil
}

// Landmark reports whether the chunk is worth remembering, with a short description.
func (f Finding) Landmark() (string, bool) {
	if f.Built >= 6 {
		desc := "постройка"
		if color, n := topColor(f.Colors); n >= 4 {
			desc = color + "ая постройка"
		}
		return fmt.Sprintf("%s (%s)", desc, strings.Join(f.Notable, ", ")), true
	}
	if f.Water >= 40 {
		return "вода, озеро или река", true
	}
	return "", false
}

func topColor(colors map[string]int) (string, int) {
	best, n := "", 0
	for c, k := range colors {
		if k > n || k == n && c < best {
			best, n = c, k
		}
	}
	return best, n
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
