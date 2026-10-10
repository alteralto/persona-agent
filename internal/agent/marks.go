package agent

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/worldmap"
)

// Marks on the map: a waypoint the player puts on Xaero's minimap or world map under one of
// these names sends the bot there -- flying when far, walking when near. Xaero keeps waypoints
// as text files (xaero/minimap/<server>/<dimension>/*.txt), one per line:
//
//	waypoint:name:initials:x:y:z:color:disabled:type:set:rotate_on_tp:tp_yaw:visibility:destination
var markNames = []string{"b", "б", "пыхтун", "цель", "bot"}

// mark is a waypoint for the bot, as last read.
type mark struct {
	name  string
	x, z  int
	fresh bool // its file changed in the last markFresh: just put on the map
}

// markBase is a waypoint name without the counter Xaero adds: "цель_1", "B 2" -> "цель", "b".
func markBase(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.TrimRight(strings.TrimRight(n, "0123456789"), "_ -#")
}

const (
	markCheckEvery = 3 * time.Second
	markTimeout    = 4 * time.Minute // a long flight or walk to a mark
	markStay       = 2 * time.Minute
	markFresh      = 10 * time.Minute // on the first look, newer marks count as new
)

// readMarks returns the waypoints for the bot in every Xaero waypoint file under dir.
func readMarks(dir string) []mark {
	var out []mark
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".txt") || strings.Contains(path, "backup") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var fileTime time.Time
		if info, err := d.Info(); err == nil {
			fileTime = info.ModTime()
		}
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Split(strings.TrimSpace(line), ":")
			if len(f) < 6 || f[0] != "waypoint" || !slices.Contains(markNames, markBase(f[1])) {
				continue
			}
			x, ex := strconv.Atoi(f[3])
			z, ez := strconv.Atoi(f[5])
			if ex != nil || ez != nil {
				continue
			}
			out = append(out, mark{name: f[1], x: x, z: z, fresh: time.Since(fileTime) < markFresh})
		}
		return nil
	})
	return out
}

// checkMarks notices a new or moved waypoint for the bot and sends it there. Marks already on
// the map when the agent starts are taken as seen: a restart does not send the bot off to an
// old one.
func (a *Agent) checkMarks(ctx context.Context) bool {
	if a.cfg.MarksDir == "" || time.Since(a.marksChecked) < markCheckEvery {
		return false
	}
	a.marksChecked = time.Now()
	marks := readMarks(a.cfg.MarksDir)
	first := a.marksSeen == nil
	if first {
		// Marks handled before a restart are kept next to the places file; without that, only
		// marks put on the map in the last few minutes are new.
		a.marksSeen = map[string]bool{}
		if raw, err := os.ReadFile(a.marksPath()); err == nil {
			for _, k := range strings.Split(string(raw), "\n") {
				if k = strings.TrimSpace(k); k != "" {
					a.marksSeen[k] = true
				}
			}
		} else {
			for _, m := range marks {
				if !m.fresh {
					a.marksSeen[m.key()] = true
				}
			}
		}
	}
	for _, m := range marks {
		if a.marksSeen[m.key()] {
			continue
		}
		a.marksSeen[m.key()] = true
		a.saveMarksSeen()
		return a.goToMark(ctx, m)
	}
	if first {
		a.saveMarksSeen()
	}
	return false
}

// goToMark flies (far) or walks (near) to a mark and says so.
func (a *Agent) goToMark(ctx context.Context, m mark) bool {
	if !a.havePos {
		return false
	}
	a.trip, a.outing, a.following, a.leading = nil, nil, "", false
	dist := math.Hypot(float64(m.x)-a.pos.X, float64(m.z)-a.pos.Z)
	flying := dist > a.flyAbove() && a.startFlight(ctx, m.x, m.z)
	if !flying {
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", m.x, m.z)}); err != nil {
			a.log.Warn("mark goto", "err", err)
			return false
		}
	}
	a.activity = &activity{kind: "mark", x: m.x, z: m.z, label: m.name, started: time.Now()}
	a.lastRequest = time.Now()
	a.log.Info("mark on the map", "name", m.name, "x", m.x, "z", m.z, "dist", int(dist), "flying", flying)
	how := "идёшь"
	if flying {
		how = "летишь на элитрах"
	}
	a.enqueue(replyJob{
		prompt: fmt.Sprintf("Тебе отметили точку на карте, %d блоков отсюда, и ты уже %s туда. "+
			"Скажи об этом одной короткой фразой до 90 символов, в характере, без координат.", int(dist), how),
		at: time.Now(),
	})
	return true
}

// mapEvery: how often the map page is redrawn.
const mapEvery = 3 * time.Minute

// writeMap redraws the map page (worldmap) from the places file every few minutes.
func (a *Agent) writeMap() {
	if a.places == nil || time.Since(a.mapWritten) < mapEvery {
		return
	}
	a.mapWritten = time.Now()
	path := a.cfg.MapPath
	if path == "" {
		path = filepath.Join(filepath.Dir(a.cfg.PlacesPath), "map.html")
	}
	page, err := worldmap.Render(a.places, time.Now())
	if err != nil {
		return // nothing seen yet
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, page, 0o644); err != nil {
		a.log.Warn("map", "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		a.log.Warn("map", "err", err)
	}
}

func (m mark) key() string { return fmt.Sprintf("%s:%d:%d", m.name, m.x, m.z) }

func (a *Agent) marksPath() string {
	return filepath.Join(filepath.Dir(a.cfg.PlacesPath), "marks-seen.txt")
}

func (a *Agent) saveMarksSeen() {
	keys := make([]string, 0, len(a.marksSeen))
	for k := range a.marksSeen {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if err := os.WriteFile(a.marksPath(), []byte(strings.Join(keys, "\n")+"\n"), 0o644); err != nil {
		a.log.Warn("marks seen", "err", err)
	}
}
