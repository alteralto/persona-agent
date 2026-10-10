package explore

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Surface kinds, one letter per block column: what the map is drawn from.
const (
	KindUnknown = '.'
	KindWater   = 'W'
	KindLava    = 'L'
	KindSand    = 'S'
	KindSnow    = 'I'
	KindLeaves  = 'F'
	KindGrass   = 'G'
	KindStone   = 'R'
	KindPath    = 'P'
	KindBuilt   = 'B'
	KindOther   = 'O'
)

// SurfaceKind sorts a top block, by its Russian name as the mod reports it, into a map kind.
func SurfaceKind(block string) byte {
	n := strings.ToLower(block)
	switch {
	case strings.Contains(n, "вода"):
		return KindWater
	case strings.Contains(n, "лава"):
		return KindLava
	case containsAny(n, builtWords):
		return KindBuilt
	case containsAny(n, []string{"песок", "песчаник"}):
		return KindSand
	case containsAny(n, []string{"снег", "лёд", "лед"}):
		return KindSnow
	case containsAny(n, []string{"листв", "листь", "лоза", "лиана"}):
		return KindLeaves
	case containsAny(n, []string{"тропинк", "гравий"}):
		return KindPath
	case containsAny(n, []string{"камень", "андезит", "диорит", "гранит", "туф", "сланец"}):
		return KindStone
	case containsAny(n, []string{"трав", "дёрн", "дерн", "земл", "мох", "цвет", "папоротник", "одуванч", "мак"}):
		return KindGrass
	}
	return KindOther
}

// AddSurface records the top blocks around pos (the mod's getSurfaceMap: dx/dz from the bot)
// into 16x16 chunk rows of kind letters. Unknown columns stay '.'.
func (p *Places) AddSurface(pos Pos, raw string) error {
	var cols []Column
	if err := json.Unmarshal([]byte(raw), &cols); err != nil {
		return err
	}
	if p.Surface == nil {
		p.Surface = map[string]string{}
	}
	bx, bz := int(math.Floor(pos.X)), int(math.Floor(pos.Z))
	for _, c := range cols {
		dx, ex := strconv.Atoi(c.DX)
		dz, ez := strconv.Atoi(c.DZ)
		if ex != nil || ez != nil {
			continue
		}
		x, z := bx+dx, bz+dz
		ch := Chunk{x >> 4, z >> 4}
		k := key(ch)
		row := []byte(p.Surface[k])
		if len(row) != 256 {
			row = []byte(strings.Repeat(".", 256))
		}
		row[(z&15)*16+(x&15)] = SurfaceKind(c.Block)
		p.Surface[k] = string(row)
	}
	return nil
}

// SurfaceAt is the kind of the column at block x, z, '.' when not seen.
func (p *Places) SurfaceAt(x, z int) byte {
	row := p.Surface[key(Chunk{x >> 4, z >> 4})]
	if len(row) != 256 {
		return KindUnknown
	}
	return row[(z&15)*16+(x&15)]
}

// SurfaceChunks lists the chunks with any surface recorded.
func (p *Places) SurfaceChunks() []Chunk {
	out := make([]Chunk, 0, len(p.Surface))
	for k := range p.Surface {
		var c Chunk
		if _, err := fmt.Sscanf(k, "%d,%d", &c.X, &c.Z); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// Track is one flight as flown: points every couple of seconds.
type Track struct {
	Style  string    `json:"style,omitempty"`
	At     time.Time `json:"at"`
	Points [][2]int  `json:"points"`
}

// maxTracks: the latest flights kept for the map.
const maxTracks = 40

// AddTrack keeps a flown track (two points at least), the latest maxTracks.
func (p *Places) AddTrack(t Track) {
	if len(t.Points) < 2 {
		return
	}
	p.Tracks = append(p.Tracks, t)
	if len(p.Tracks) > maxTracks {
		p.Tracks = p.Tracks[len(p.Tracks)-maxTracks:]
	}
}
