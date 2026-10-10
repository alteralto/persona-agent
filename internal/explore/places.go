package explore

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Place is a remembered spot. Stored as one JSON file per world so it survives restarts.
type Place struct {
	Desc string    `json:"desc"`
	X    int       `json:"x"`
	Z    int       `json:"z"`
	Seen time.Time `json:"seen"`
}

type Places struct {
	path    string
	Home    *Pos            `json:"home,omitempty"`
	Visited map[string]bool `json:"visited"`
	List    []Place         `json:"places"`
	// Surface: per chunk ("x,z"), 256 kind letters, row by row (z then x): the map's ground.
	Surface map[string]string `json:"surface,omitempty"`
	// Tracks: the latest flights as flown.
	Tracks []Track `json:"tracks,omitempty"`
}

func LoadPlaces(path string) (*Places, error) {
	p := &Places{path: path, Visited: map[string]bool{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if p.Visited == nil {
		p.Visited = map[string]bool{}
	}
	return p, nil
}

func (p *Places) Save() error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(p, "", "  ")
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

func key(c Chunk) string { return fmt.Sprintf("%d,%d", c.X, c.Z) }

func (p *Places) IsVisited(c Chunk) bool { return p.Visited[key(c)] }

func (p *Places) MarkVisited(c Chunk) { p.Visited[key(c)] = true }

// VisitedSet converts the stored keys for Next.
func (p *Places) VisitedSet() map[Chunk]bool {
	out := make(map[Chunk]bool, len(p.Visited))
	for k := range p.Visited {
		var c Chunk
		if _, err := fmt.Sscanf(k, "%d,%d", &c.X, &c.Z); err == nil {
			out[c] = true
		}
	}
	return out
}

// Add records a place unless one with the same description is already within 24 blocks.
func (p *Places) Add(pl Place) bool {
	for _, q := range p.List {
		if q.Desc == pl.Desc && math.Hypot(float64(q.X-pl.X), float64(q.Z-pl.Z)) < 24 {
			return false
		}
	}
	p.List = append(p.List, pl)
	return true
}

// Describe lists up to n places nearest to from, with distance and direction, for the prompt.
func (p *Places) Describe(from Pos, n int) string {
	list := append([]Place(nil), p.List...)
	dist := func(q Place) float64 { return math.Hypot(float64(q.X)-from.X, float64(q.Z)-from.Z) }
	sort.Slice(list, func(i, j int) bool { return dist(list[i]) < dist(list[j]) })
	if len(list) > n {
		list = list[:n]
	}
	var b strings.Builder
	for _, q := range list {
		fmt.Fprintf(&b, "- %s: x=%d z=%d, %d блоков на %s\n", q.Desc, q.X, q.Z, int(dist(q)),
			Direction(from, float64(q.X), float64(q.Z)))
	}
	return b.String()
}

// Direction names the compass side of (x,z) as seen from p. In Minecraft north is -Z.
func Direction(p Pos, x, z float64) string {
	dx, dz := x-p.X, z-p.Z
	angle := math.Atan2(dx, -dz) * 180 / math.Pi // 0 = north, 90 = east
	if angle < 0 {
		angle += 360
	}
	names := []string{"север", "северо-восток", "восток", "юго-восток", "юг", "юго-запад", "запад", "северо-запад"}
	return names[int((angle+22.5)/45)%8]
}
