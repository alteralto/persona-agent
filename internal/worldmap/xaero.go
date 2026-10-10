package worldmap

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/alteralto/persona-agent/internal/explore"
)

// XaeroMark starts the name of every waypoint the bot writes, so it can tell them from the
// player's own: those are never touched.
const XaeroMark = "◆ "

// xaeroMax: the most recent finds written; a map full of yellow dots is no map.
const xaeroMax = 60

// WriteXaero puts the bot's home and finds on Xaero's minimap and world map: into every
// overworld waypoint file (dim%0/mw*.txt) under dir, the server's folder. The player's lines stay
// as they are; the bot's own from last time are replaced. Xaero reads the files when it joins a
// world and rewrites them when it saves, so call this while the game is closed. Signs are left out.
// It returns how many files it wrote.
func WriteXaero(dir string, p *explore.Places) (int, error) {
	lines := xaeroLines(p)
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "dim%0" && strings.HasPrefix(d.Name(), "mw") && strings.HasSuffix(d.Name(), ".txt") {
			files = append(files, path)
		}
		return nil
	})
	n := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return n, err
		}
		nl := "\n"
		if strings.Contains(string(raw), "\r\n") {
			nl = "\r\n"
		}
		var out []string
		for _, l := range strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n") {
			l = strings.TrimRight(l, "\r")
			if fields := strings.SplitN(l, ":", 3); len(fields) == 3 && fields[0] == "waypoint" &&
				strings.HasPrefix(fields[1], XaeroMark) {
				continue // ours from last time
			}
			out = append(out, l)
		}
		out = append(out, lines...)
		tmp := f + ".tmp"
		if err := os.WriteFile(tmp, []byte(strings.Join(out, nl)+nl), 0o644); err != nil {
			return n, err
		}
		if err := os.Rename(tmp, f); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func xaeroLines(p *explore.Places) []string {
	y := 64
	if p.Home != nil {
		y = int(p.Home.Y)
	}
	var out []string
	if p.Home != nil {
		out = append(out, xaeroLine("Нора Пыхтуна", int(p.Home.X), y, int(p.Home.Z), 12))
	}
	var finds []explore.Place
	for _, pl := range p.List {
		if !strings.HasPrefix(pl.Desc, "табличка") {
			finds = append(finds, pl)
		}
	}
	sort.Slice(finds, func(i, j int) bool { return finds[i].Seen.After(finds[j].Seen) })
	if len(finds) > xaeroMax {
		finds = finds[:xaeroMax]
	}
	for _, f := range finds {
		out = append(out, xaeroLine(shortName(f.Desc), f.X, y, f.Z, 14))
	}
	return out
}

// xaeroLine: waypoint:name:initials:x:y:z:color:disabled:type:set:rotate_on_tp:tp_yaw:visibility:destination
func xaeroLine(name string, x, y, z, color int) string {
	initial := "?"
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			initial = string(unicode.ToUpper(r))
			break
		}
	}
	name = strings.ReplaceAll(XaeroMark+name, ":", "§§") // Xaero's escape for ':' in a name
	return fmt.Sprintf("waypoint:%s:%s:%d:%d:%d:%d:false:0:gui.xaero_default:false:0:0:false", name, initial, x, y, z, color)
}

// shortName: "серая постройка (каменные кирпичи, …)" -> "серая постройка"; a bare "постройка"
// keeps its first material ("постройка — дубовые доски") so the dots on the map differ. At most
// 32 letters.
func shortName(desc string) string {
	name, rest, _ := strings.Cut(desc, " (")
	name = strings.Join(strings.Fields(name), " ")
	if len(strings.Fields(name)) == 1 && rest != "" {
		first, _, _ := strings.Cut(strings.TrimSuffix(rest, ")"), ",")
		if first = strings.TrimSpace(first); first != "" {
			name += " — " + first
		}
	}
	if strings.Count(name, "«") > strings.Count(name, "»") {
		name += "»"
	}
	if r := []rune(name); len(r) > 32 {
		name = strings.TrimSpace(string(r[:31])) + "…"
	}
	return name
}
