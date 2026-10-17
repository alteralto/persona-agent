package agent_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/explore"
	"github.com/alteralto/persona-agent/internal/sim"
)

// Home set by x, z only sits on the top surface of its column: on the platform over the
// ground, not at the bot's own height -- it was once mid-air under a platform, and flights
// home "arrived" up there and glided off.
func TestSimHomeOnSurface(t *testing.T) {
	for _, c := range []struct {
		name     string
		platform bool
		want     float64
	}{{"platform", true, 91}, {"ground", false, 70}} {
		t.Run(c.name, func(t *testing.T) {
			var path string
			r := startSim(t, sim.Options{Fork: true}, func(cfg *agent.Config) {
				cfg.Home = &explore.Pos{X: 10, Z: 10}
				path = cfg.PlacesPath
			})
			if c.platform {
				r.w.AddPlatform(10, 10, 3, 90)
			}
			r.ready()
			r.waitFor("home on the surface", 5*time.Second, func() bool {
				b, err := os.ReadFile(path)
				if err != nil {
					return false
				}
				var p struct{ Home *explore.Pos }
				return json.Unmarshal(b, &p) == nil && p.Home != nil && p.Home.Y == c.want
			})
		})
	}
}
