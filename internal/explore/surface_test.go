package explore

import "testing"

func TestAddSurfaceAndKinds(t *testing.T) {
	p := &Places{Visited: map[string]bool{}}
	raw := `[{"dx":"0","dz":"0","block":"Вода"},{"dx":"1","dz":"0","block":"Дубовые доски"},` +
		`{"dx":"-1","dz":"0","block":"Блок травы"},{"dx":"0","dz":"-1","block":"Песок"}]`
	if err := p.AddSurface(Pos{X: -0.5, Z: 15.2}, raw); err != nil {
		t.Fatal(err)
	}
	// The bot stands in block -1, 15: the columns are around it, across a chunk border.
	for _, c := range []struct {
		x, z int
		want byte
	}{{-1, 15, KindWater}, {0, 15, KindBuilt}, {-2, 15, KindGrass}, {-1, 14, KindSand}, {5, 5, KindUnknown}} {
		if got := p.SurfaceAt(c.x, c.z); got != c.want {
			t.Errorf("%d,%d: got %c, want %c", c.x, c.z, got, c.want)
		}
	}
	if n := len(p.SurfaceChunks()); n != 2 {
		t.Errorf("%d chunks, want 2 (both sides of x = 0)", n)
	}
}
