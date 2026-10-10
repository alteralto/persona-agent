package worldmap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

func TestWriteXaero(t *testing.T) {
	dir := t.TempDir()
	dim := filepath.Join(dir, "dim%0")
	nether := filepath.Join(dir, "dim%-1")
	for _, d := range []string{dim, nether} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(dim, "mw$default_1.txt")
	user := "#\r\n#waypoint:name:initials:x:y:z:color:disabled:type:set:rotate_on_tp:tp_yaw:visibility_type:destination\r\n#\r\n" +
		"waypoint:цель_1:Ц:-596:91:-265:10:false:0:gui.xaero_default:false:0:0:false\r\n" +
		"waypoint:◆ старая находка:С:1:64:1:14:false:0:gui.xaero_default:false:0:0:false\r\n"
	os.WriteFile(file, []byte(user), 0o644)
	netherFile := filepath.Join(nether, "mw$default_1.txt")
	os.WriteFile(netherFile, []byte("waypoint:портал:П:1:2:3:0:false:0:gui.xaero_default:false:0:0:false\n"), 0o644)

	p, err := explore.LoadPlaces(filepath.Join(t.TempDir(), "places.json"))
	if err != nil {
		t.Fatal(err)
	}
	p.Home = &explore.Pos{X: -471, Y: 103.8, Z: -183}
	now := time.Now()
	p.Add(explore.Place{Desc: "серая постройка (каменные кирпичи, светло-серое стекло)", X: -397, Z: -182, Seen: now})
	p.Add(explore.Place{Desc: "табличка «Ферма тыкв»", X: 5, Z: 5, Seen: now})
	p.Add(explore.Place{Desc: "башня: высокая", X: 10, Z: 20, Seen: now.Add(-time.Minute)})
	p.Add(explore.Place{Desc: "постройка (дубовые доски, стекло)", X: 30, Z: 40, Seen: now.Add(-2 * time.Minute)})

	n, err := WriteXaero(dir, p)
	if err != nil || n != 1 {
		t.Fatalf("WriteXaero = %d, %v; want 1 file", n, err)
	}
	got, _ := os.ReadFile(file)
	s := string(got)
	for _, want := range []string{
		"waypoint:цель_1:Ц:-596:91:-265:10:false:0:gui.xaero_default:false:0:0:false\r\n", // the player's: untouched
		"waypoint:◆ Нора Пыхтуна:Н:-471:103:-183:12:",
		"waypoint:◆ серая постройка:С:-397:103:-182:14:",
		"waypoint:◆ башня§§ высокая:Б:10:103:20:14:",
		"waypoint:◆ постройка — дубовые доски:П:30:103:40:14:",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	for _, gone := range []string{"старая находка", "Ферма тыкв"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q should not be there:\n%s", gone, s)
		}
	}
	if strings.Count(s, "\r\n") != strings.Count(s, "\n") {
		t.Error("line endings mixed")
	}
	// Twice in a row: the same file, no duplicates.
	WriteXaero(dir, p)
	again, _ := os.ReadFile(file)
	if string(again) != s {
		t.Errorf("a second write changed the file:\n%s", again)
	}
	if b, _ := os.ReadFile(netherFile); strings.Contains(string(b), "◆") {
		t.Error("wrote into the Nether")
	}
}
