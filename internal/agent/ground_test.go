package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
	"github.com/alteralto/persona-agent/internal/mcp"
)

func TestPublicWalkOnSkyBars(t *testing.T) {
	var sample atomic.Value
	sample.Store(`{"self":{"onGround":true,"pitch":62.9,"vel":"0.00,-0.08,0.15"}}`)
	commands := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Params struct {
				Name      string
				Arguments struct{ Cmd string }
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			t.Error(err)
			return
		}
		if call.Params.Name == "ExecuteCommand" {
			commands <- call.Params.Arguments.Cmd
		}
		encoded, _ := json.Marshal(sample.Load())
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":%s}]}}`, encoded)
	}))
	defer server.Close()
	looks := make(chan bool, 1)
	a := &Agent{cfg: Config{PublicServer: true, LevelWalkView: func(context.Context) error { looks <- true; return nil }},
		mc: mcp.New(server.URL, "test"), log: slog.Default(), fork: true, trip: &trip{}}
	ctx := context.Background()
	a.checkGroundMovement(ctx, true)
	select {
	case <-looks:
	case <-time.After(time.Second):
		t.Fatal("walking with gaze down did not raise the view")
	}
	a.lastViewCorrection = time.Time{}
	a.checkGroundMovement(ctx, false)
	select {
	case <-looks:
	case <-time.After(time.Second):
		t.Fatal("arrival left the camera pointed down")
	}
	sample.Store(`{"self":{"onGround":false,"pitch":51,"vel":"0.00,-1.78,0.00"}}`)
	if !a.checkGroundMovement(ctx, true) || a.trip != nil {
		t.Fatal("continued planning an XZ route in the air")
	}
	if !a.checkGroundMovement(ctx, false) || len(commands) != 1 {
		t.Fatal("expected one stop for the whole launch")
	}
	if cmd := <-commands; cmd != "@stop" {
		t.Fatalf("unexpected command %s", cmd)
	}
	sample.Store(`{"self":{"onGround":true,"pitch":4}}`)
	if a.checkGroundMovement(ctx, false) || a.waitForGround {
		t.Fatal("failed to resume on landing")
	}

	// Two different XZ routes failed from the same corner. The short escape must
	// point to a previously occupied floor cell, including its actual elevation.
	pos := explore.Pos{X: -1712, Y: 88, Z: 3544.5}
	a.places = &explore.Places{Visited: map[string]bool{}}
	a.walkTrail = []explore.Pos{{X: -1717.3, Y: 88, Z: 3544.5}, pos}
	for i := 0; i < 2; i++ {
		a.trip = &trip{target: explore.Chunk{X: -108 + i, Z: 221}}
		a.navGaveUp = time.Now()
		a.checkStuck(ctx, pos, true)
		if cmd := <-commands; cmd != "@stop" {
			t.Fatal(cmd)
		}
	}
	if a.activity == nil || a.activity.kind != "backtrack" {
		t.Fatal("did not leave the corner")
	}
	if cmd := <-commands; cmd != "@goto -1718 88 3544" {
		t.Fatal(cmd)
	}
}

func TestLevelDetourPastSkyBarsHedge(t *testing.T) {
	// The body was at (-1719, 88, 3524). Tungsten kept climbing the leaves
	// immediately east. The three cells north were open at feet and head height.
	pos := explore.Pos{X: -1718.3, Y: 88, Z: 3524.6}
	blocks := []map[string]string{
		{"position": "-1718, 87, 3524", "block": "дёрн"}, {"position": "-1718, 88, 3524", "block": "дубовые листья"},
		{"position": "-1719, 87, 3523", "block": "замшелые каменные кирпичи"},
		{"position": "-1719, 87, 3522", "block": "замшелые каменные кирпичи"},
		{"position": "-1719, 87, 3521", "block": "камень"},
	}
	raw, _ := json.Marshal(blocks)
	p, ok := levelDetour(string(raw), pos, -1716, 3524, nil)
	if !ok || p != [3]int{-1719, 88, 3521} {
		t.Fatalf("detour %v, found %t", p, ok)
	}
	blocks = append(blocks, map[string]string{"position": "-1719, 89, 3522", "block": "stone"})
	raw, _ = json.Marshal(blocks)
	if _, ok := levelDetour(string(raw), pos, -1716, 3524, nil); ok {
		t.Fatal("accepted a path with no headroom")
	}
}

func TestPublicDetourThroughCrowd(t *testing.T) {
	// Two flat lanes, east toward the goal and north away from it. A player
	// standing halfway down the east lane must affect this walk only.
	var blocks []map[string]string
	for step := 1; step <= 4; step++ {
		for _, p := range [][2]int{{step, 0}, {0, -step}} {
			blocks = append(blocks, map[string]string{"position": fmt.Sprintf("%d,69,%d", p[0], p[1]), "block": "stone"})
		}
	}
	terrain, _ := json.Marshal(blocks)
	for _, tc := range []struct{ name, body, want string }{
		{"in lane", "2.5,70,0.5", "@goto 0 70 -3"},
		{"on balcony", "2.5,74,0.5", "@goto 4 70 0"},
		{"walked away", "7.5,70,0.5", "@goto 4 70 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			people := []map[string]string{{"name": "Near", "pos": tc.body}}
			for i := 1; i < 100; i++ {
				people = append(people, map[string]string{"name": fmt.Sprintf("Player%d", i), "pos": fmt.Sprintf("%d,70,20", i)})
			}
			state, _ := json.Marshal(map[string]any{"self": map[string]any{"onGround": true}, "players": people})
			commands := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var call struct {
					Params struct {
						Name      string
						Arguments struct{ Cmd string }
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
					t.Error(err)
					return
				}
				body := string(state)
				switch call.Params.Name {
				case "getBlocksAround":
					body = string(terrain)
				case "ExecuteCommand":
					commands <- call.Params.Arguments.Cmd
					body = "ok"
				}
				encoded, _ := json.Marshal(body)
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":%s}]}}`, encoded)
			}))
			defer server.Close()
			a := &Agent{cfg: Config{PublicServer: true}, fork: true, mc: mcp.New(server.URL, "sim"), log: slog.Default(), pos: explore.Pos{X: 0.5, Y: 70, Z: 0.5}}
			ctx := context.Background()
			a.checkGroundMovement(ctx, true)
			if !a.tryLevelDetour(ctx, a.pos, 20, 0) {
				t.Fatal("missed the clear lane")
			}
			if got := <-commands; got != tc.want {
				t.Fatalf("detour %q; want %q", got, tc.want)
			}
		})
	}
}
