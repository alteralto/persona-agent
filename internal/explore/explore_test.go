package explore

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePos(t *testing.T) {
	cases := []struct {
		in   string
		want Pos
		ok   bool
	}{
		{"-435,6,70,0,-134,2", Pos{-435.6, 70.0, -134.2}, true}, // Russian locale
		{"-435.6,70.0,-134.2", Pos{-435.6, 70.0, -134.2}, true},
		{"10,64,-3", Pos{10, 64, -3}, true},
		{"garbage", Pos{}, false},
		{"1,2", Pos{}, false},
	}
	for _, c := range cases {
		got, err := ParsePos(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("ParsePos(%q) = %+v, %v; want %+v ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestChunkOfNegative(t *testing.T) {
	if c := ChunkOf(Pos{X: -0.5, Z: -16.1}); c != (Chunk{-1, -2}) {
		t.Errorf("ChunkOf negative = %+v", c)
	}
}

func TestNextVisitsAllThenStops(t *testing.T) {
	home := Chunk{0, 0}
	visited := map[Chunk]bool{home: true}
	cur := home
	for i := 0; i < 8; i++ {
		c, ok := Next(home, cur, 1, visited)
		if !ok {
			t.Fatalf("stopped early after %d", i)
		}
		if c.X < -1 || c.X > 1 || c.Z < -1 || c.Z > 1 {
			t.Fatalf("left radius: %+v", c)
		}
		visited[c], cur = true, c
	}
	if _, ok := Next(home, cur, 1, visited); ok {
		t.Error("expected exhausted area")
	}
}

func TestSummarizePinkHouse(t *testing.T) {
	raw := `[` +
		strings.Repeat(`{"dx":"0","dz":"0","block":"Розовая шерсть"},`, 5) +
		`{"dx":"1","dz":"0","block":"Дубовые доски"},` +
		`{"dx":"2","dz":"0","block":"Стекло"},` +
		`{"dx":"3","dz":"0","block":"дёрн"},` +
		`{"dx":"4","dz":"0","block":"вода"}]`
	f, err := Summarize(raw)
	if err != nil {
		t.Fatal(err)
	}
	if f.Built != 7 || f.Water != 1 || f.Colors["розов"] != 5 {
		t.Fatalf("finding = %+v", f)
	}
	desc, ok := f.Landmark()
	if !ok || !strings.HasPrefix(desc, "розовая постройка") {
		t.Errorf("Landmark = %q, %v", desc, ok)
	}
}

func TestSummarizeNatureIsNotLandmark(t *testing.T) {
	f, _ := Summarize(`[{"block":"дёрн"},{"block":"мак"},{"block":"бедрок"}]`)
	if _, ok := f.Landmark(); ok {
		t.Error("grass and flowers are not a landmark")
	}
}

func TestDirection(t *testing.T) {
	p := Pos{}
	for _, c := range []struct {
		x, z float64
		want string
	}{{0, -10, "север"}, {10, 0, "восток"}, {0, 10, "юг"}, {-10, 0, "запад"}, {10, -10, "северо-восток"}} {
		if got := Direction(p, c.x, c.z); got != c.want {
			t.Errorf("Direction(%v,%v) = %s, want %s", c.x, c.z, got, c.want)
		}
	}
}

func TestPlacesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "places.json")
	p, err := LoadPlaces(path)
	if err != nil {
		t.Fatal(err)
	}
	p.MarkVisited(Chunk{-28, -9})
	if !p.Add(Place{Desc: "розовая постройка", X: -440, Z: -140}) {
		t.Fatal("first add rejected")
	}
	if p.Add(Place{Desc: "розовая постройка", X: -450, Z: -145}) {
		t.Error("near duplicate accepted")
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	q, err := LoadPlaces(path)
	if err != nil {
		t.Fatal(err)
	}
	if !q.IsVisited(Chunk{-28, -9}) || len(q.List) != 1 || !q.VisitedSet()[Chunk{-28, -9}] {
		t.Errorf("reloaded = %+v", q)
	}
	if d := q.Describe(Pos{X: -440, Z: -100}, 3); !strings.Contains(d, "40 блоков на север") {
		t.Errorf("Describe = %q", d)
	}
}

func TestColorAdj(t *testing.T) {
	for stem, want := range map[string]string{"лайм": "лаймовая", "син": "синяя", "розов": "розовая",
		"оранж": "оранжевая", "коричн": "коричневая", "пурпур": "пурпурная", "чёрн": "чёрная"} {
		if got := colorAdj(stem); got != want {
			t.Errorf("colorAdj(%q) = %q, want %q", stem, got, want)
		}
	}
}
