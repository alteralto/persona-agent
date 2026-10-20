package agent_test

import (
	"encoding/json"
	"os"
	"strings"
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

// Down a pit the elytra cannot take off ("under a roof"): the way out is creative flight, and
// then the follow goes on.
func TestSimHoversOutOfPit(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10, Elytra: true}, nil)
	r.w.AddPit(0, 0, 2)
	r.w.AddPlayer("Alex", 25, 0)
	r.ready()
	r.w.Say("Alex", "Пыхтун, иди за мной")
	r.waitFor("@follow Alex", 3*time.Second, func() bool { return r.w.Has("cmd", "@follow Alex") })
	r.waitFor("creative flight out", 20*time.Second, func() bool { return r.w.Has("tool", "hoverTo") })
	r.waitFor("out of the pit, next to Alex", 5*time.Second, func() bool { return r.w.DistanceTo("Alex") < 4 })
}

// Asked to fly from a spot the elytra cannot leave (letters hanging under a platform): creative
// flight there at once, not a walk that goes nowhere till the stuck check picks it up.
func TestSimFlightFromUnderRoofHovers(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10, Elytra: true}, nil)
	r.w.AddPit(0, 0, 2)
	r.w.AddPlayer("Alex", 300, 40)
	r.ready()
	r.w.Say("Alex", "Пыхтун, прилетай ко мне")
	r.waitFor("take-off tried", 3*time.Second, func() bool { return r.w.Has("fly", "300 40") })
	r.waitFor("creative flight instead", 3*time.Second, func() bool { return r.w.Has("tool", "hoverTo") })
	if !strings.Contains(r.log.String(), "no open sky near, hovering there") {
		t.Fatal("hoverTo came from somewhere else, not the failed take-off")
	}
	r.waitFor("next to Alex", 10*time.Second, func() bool { return r.w.DistanceTo("Alex") < 4 })
}

// A riddle about a place it knows: the first player to get there is congratulated and gets a
// present by @give.
func TestSimRiddleWinnerGetsPresent(t *testing.T) {
	var path string
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10}, func(cfg *agent.Config) {
		path = cfg.PlacesPath
		raw, _ := json.Marshal(map[string]any{"places": []map[string]any{
			{"desc": "вода, озеро или река", "x": 60, "z": 10},
			{"desc": "табличка «привет»", "x": 50, "z": 0}, // signs make poor riddles
		}})
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	})
	r.w.AddPlayer("Alex", 5, 0)
	r.ready()
	r.w.Say("Alex", "Пыхтун, загадай загадку!")
	r.waitFor("riddle asked", 3*time.Second, func() bool { return strings.Contains(r.log.String(), "game: start") })
	if !strings.Contains(r.log.String(), "озеро") {
		t.Fatal("riddle not about the lake")
	}
	r.w.AddPlayer("Alex", 61, 12) // there
	r.waitFor("present for Alex", 5*time.Second, func() bool { return r.w.Has("cmd", "@give Alex") })
}

// gamePlaces writes the places the bot knows before it starts.
func gamePlaces(t *testing.T, places ...map[string]any) func(cfg *agent.Config) {
	return func(cfg *agent.Config) {
		raw, _ := json.Marshal(map[string]any{"places": places})
		if err := os.WriteFile(cfg.PlacesPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// While a riddle is on, a searcher who moves nearer the place hears "warmer" at once.
func TestSimRiddleWarmerColder(t *testing.T) {
	defer agent.ShortGames(time.Minute, time.Minute, 0, time.Minute, time.Minute)()
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10},
		gamePlaces(t, map[string]any{"desc": "вода, озеро или река", "x": 60, "z": 10}))
	r.w.AddPlayer("Alex", 5, 0) // 56 blocks off: cool
	r.ready()
	r.w.Say("Alex", "Пыхтун, загадай загадку!")
	r.waitFor("riddle asked", 3*time.Second, func() bool { return strings.Contains(r.log.String(), "game: start") })
	time.Sleep(time.Second)      // seen where they start from
	r.w.AddPlayer("Alex", 35, 5) // 25 off: warm
	r.waitFor("warmer", 3*time.Second, func() bool { return strings.Contains(r.log.String(), `msg="riddle: heat" player=Alex band=2 was=1`) })
}

// A riddle nobody solves: it says what the place was, flies there and fires a rocket over it.
func TestSimRiddleBeacon(t *testing.T) {
	defer agent.ShortGames(2*time.Second, time.Minute, time.Minute, time.Minute, time.Minute)()
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10, Elytra: true},
		gamePlaces(t, map[string]any{"desc": "вода, озеро или река", "x": 60, "z": 10}))
	r.w.AddPlayer("Alex", 5, 0)
	r.ready()
	r.w.Say("Alex", "Пыхтун, загадай загадку!")
	r.waitFor("nobody won", 6*time.Second, func() bool { return strings.Contains(r.log.String(), "game: nobody won") })
	r.waitFor("to the place", 3*time.Second, func() bool { return r.w.Has("fly", "60 10") })
	r.waitFor("rocket over it", 8*time.Second, func() bool { return strings.Contains(r.log.String(), "beacon: lit") })
}

// The treasure: it flies off to a place, drops the present there, comes back and sets the riddle;
// the finder keeps what lies there (no @give).
func TestSimTreasure(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10, Elytra: true},
		gamePlaces(t, map[string]any{"desc": "вода, озеро или река", "x": 60, "z": 10}))
	r.w.AddPlayer("Alex", 5, 0)
	r.ready()
	r.w.Say("Alex", "Пыхтун, давай поиграем в клад!")
	r.waitFor("off to hide it", 3*time.Second, func() bool { return r.w.Has("fly", "60 10") })
	r.waitFor("dropped there", 8*time.Second, func() bool { return r.w.Has("tool", "dropItem") })
	r.waitFor("back to the players", 3*time.Second, func() bool { return r.w.Has("fly", "0 0") })
	r.waitFor("the search on", 8*time.Second, func() bool {
		return strings.Contains(r.log.String(), `msg="treasure: search on" dropped=true`)
	})
	r.w.AddPlayer("Alex", 61, 12) // found it
	r.waitFor("Alex wins", 5*time.Second, func() bool { return strings.Contains(r.log.String(), "winner=Alex") })
	time.Sleep(200 * time.Millisecond)
	if r.w.Has("cmd", "@give Alex") {
		t.Fatal("handed over by @give: the treasure on the ground is the present")
	}
}

// A race: the bot judges, not races. It flies to the finish, gives the start from there, and the
// first player at the finish wins.
func TestSimRace(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10, Elytra: true},
		gamePlaces(t, map[string]any{"desc": "серая постройка", "x": 90, "z": 0}))
	r.w.AddPlayer("Alex", 5, 0)
	r.ready()
	r.w.Say("Alex", "Пыхтун, давай гонку!")
	r.waitFor("race announced", 3*time.Second, func() bool { return strings.Contains(r.log.String(), "game: start") })
	if r.w.Has("say", "марш") {
		t.Fatal("started before the judge got to the finish")
	}
	r.waitFor("judge to the finish", 3*time.Second, func() bool { return r.w.Has("fly", "90 0") })
	r.waitFor("start from the finish", 10*time.Second, func() bool { return r.w.Has("say", "марш") })
	if !strings.Contains(r.log.String(), `msg="race: start" at_finish=true`) {
		t.Fatal("the start was not given from the finish")
	}
	r.waitFor("a rocket for the start", 2*time.Second, func() bool { return r.w.Has("salute", "") })
	r.w.AddPlayer("Alex", 88, 2) // there
	r.waitFor("Alex wins", 5*time.Second, func() bool { return strings.Contains(r.log.String(), "winner=Alex") })
}
