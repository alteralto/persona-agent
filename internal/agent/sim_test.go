package agent_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/giga"
	"github.com/alteralto/persona-agent/internal/laya"
	"github.com/alteralto/persona-agent/internal/mcp"
	"github.com/alteralto/persona-agent/internal/sim"
)

// End-to-end runs of the real agent against the simulated game (internal/sim). They cover what
// unit tests cannot: the order of tool calls, flights landing, state after a game restart.

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type run struct {
	t    *testing.T
	w    *sim.World
	giga *sim.Giga
	log  *lockedBuf
}

func startSim(t *testing.T, opt sim.Options, tweak func(*agent.Config)) *run {
	t.Helper()
	opt.BotNick = "Pyhtun"
	w := sim.New(opt)
	g := &sim.Giga{}
	game := httptest.NewServer(w.Handler())
	ls := httptest.NewServer(sim.Laya())
	gs := httptest.NewServer(g.Handler())
	cfg := agent.Config{
		BotNick:        "Pyhtun",
		Aliases:        []string{"пыхтун"},
		Creative:       opt.Mode != "survival",
		MinConfidence:  0.45,
		PollEvery:      20 * time.Millisecond,
		RequireMention: true,
		ReplyEvery:     10 * time.Millisecond,
		ExploreAfter:   time.Hour,
		PlacesPath:     filepath.Join(t.TempDir(), "places.json"),
		ProtectRadius:  256,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	buf := &lockedBuf{}
	log := slog.New(slog.NewTextHandler(buf, nil))
	llm := giga.New("sim", "GIGACHAT_API_PERS", "GigaChat-2", true).
		WithEndpoints(gs.URL+"/oauth", gs.URL+"/chat/completions")
	a := agent.New(cfg, mcp.New(game.URL, "sim"), laya.New(ls.URL), llm, log)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := a.Run(ctx); err != nil {
			t.Errorf("agent stopped: %v", err)
		}
	}()
	r := &run{t: t, w: w, giga: g, log: buf}
	t.Cleanup(func() {
		cancel()
		<-done
		time.Sleep(50 * time.Millisecond) // let the reply worker drop its last call
		game.Close()
		ls.Close()
		gs.Close()
		if t.Failed() {
			t.Logf("agent log:\n%s", buf.String())
			for _, e := range w.Events() {
				t.Logf("%6dms %-5s %s", e.At.Milliseconds(), e.Kind, e.Text)
			}
		}
	})
	return r
}

// waitFor polls cond for up to d.
func (r *run) waitFor(what string, d time.Duration, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for %s", what)
}

func (r *run) ready() {
	r.t.Helper()
	r.waitFor("build policy", 3*time.Second, func() bool { return r.w.Has("tool", "setBuildPolicy") ||
		r.w.Has("tool", "markProtectedArea") })
}

func TestSimFollowOnFoot(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10}, nil)
	r.w.AddPlayer("Alex", 12, 0)
	r.ready()
	r.w.Say("Alex", "Пыхтун, иди за мной")
	r.waitFor("@follow Alex", 3*time.Second, func() bool { return r.w.Has("cmd", "@follow Alex") })
	r.waitFor("bot next to Alex", 3*time.Second, func() bool { return r.w.DistanceTo("Alex") < 3 })
	r.waitFor("reply about following", 3*time.Second, func() bool { return r.w.Has("say", "следом") })
	if r.w.Has("fly", "") {
		t.Error("flew 12 blocks: should walk")
	}
}

func TestSimFlyToPlayer(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10}, nil)
	r.w.AddPlayer("Alex", 300, 40)
	r.ready()
	r.w.Say("Alex", "Пыхтун, прилетай ко мне")
	r.waitFor("take-off", 3*time.Second, func() bool { return r.w.Has("fly", "300 40") })
	r.waitFor("landing", 5*time.Second, func() bool { return r.w.Has("land", "") })
	r.waitFor("@follow after landing", 3*time.Second, func() bool { return r.w.Has("cmd", "@follow Alex") })
	r.waitFor("bot next to Alex", 3*time.Second, func() bool { return r.w.DistanceTo("Alex") < 3 })
	if !r.w.Has("tool", `creativeGive {"count":1,"item":"elytra"}`) {
		t.Error("no elytra taken from the creative tab")
	}
}

func TestSimFullInventoryTidiedBeforeKit(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 0}, nil)
	r.w.AddPlayer("Alex", 300, 0)
	r.ready()
	r.w.Say("Alex", "Пыхтун, прилетай ко мне")
	r.waitFor("take-off", 3*time.Second, func() bool { return r.w.Has("fly", "") })
	ev := r.w.Events()
	tidy, give := -1, -1
	for i, e := range ev {
		if e.Kind == "tool" && strings.HasPrefix(e.Text, "tidyInventory") && tidy < 0 {
			tidy = i
		}
		if e.Kind == "tool" && strings.HasPrefix(e.Text, "creativeGive") && give < 0 {
			give = i
		}
	}
	if tidy < 0 || give < 0 || tidy > give {
		t.Fatalf("want tidyInventory before creativeGive, got tidy=%d give=%d", tidy, give)
	}
}

func TestSimSurvivalWithoutForkWalks(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "survival", Fork: false}, nil)
	r.w.AddPlayer("Alex", 100, 0)
	r.ready()
	r.w.Say("Alex", "Пыхтун, прилетай ко мне")
	r.waitFor("@follow instead of a flight", 3*time.Second, func() bool { return r.w.Has("cmd", "@follow Alex") })
	if r.w.Has("tool", "flyTo") {
		t.Error("called flyTo on the upstream mod")
	}
	if !r.w.Has("tool", "markProtectedArea") {
		t.Error("upstream mod: no protected zone around home")
	}
}

func TestSimStopHoldsAgainstCompanion(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10}, func(c *agent.Config) {
		c.Companions = []string{"NetTyan"}
		c.OtherBots = []string{"NetTyan"}
		c.ExploreAfter = 100 * time.Millisecond
	})
	r.w.AddPlayer("NetTyan", 10, 0)
	r.w.AddPlayer("Alex", 0, 5)
	r.ready()
	r.waitFor("companion follow", 3*time.Second, func() bool { return r.w.Has("cmd", "@follow NetTyan") })
	r.w.Say("Alex", "Пыхтун, стой")
	r.waitFor("@stop", 3*time.Second, func() bool { return r.w.Has("cmd", "@stop") })
	before := r.w.Count("cmd", "@follow NetTyan")
	time.Sleep(800 * time.Millisecond)
	if after := r.w.Count("cmd", "@follow NetTyan"); after != before {
		t.Errorf("went back to the companion %d time(s) right after «стой»", after-before)
	}
}

func TestSimGameRestartReappliesPolicy(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10}, nil)
	r.ready()
	r.waitFor("fall saves set", 3*time.Second, func() bool { return r.w.Has("cmd", "@set autoMLGBucket") })
	r.w.SetDown(true)
	time.Sleep(300 * time.Millisecond)
	r.w.SetDown(false)
	r.waitFor("policy again", 3*time.Second, func() bool { return r.w.Count("tool", "setBuildPolicy") >= 2 })
	r.waitFor("fall saves again", 3*time.Second, func() bool { return r.w.Count("cmd", "@set autoMLGBucket") >= 2 })
	if n := strings.Count(r.log.String(), "game connection lost"); n != 1 {
		t.Errorf("logged the outage %d times, want once", n)
	}
}

func TestSimStartsBeforeGame(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true}, nil)
	r.w.SetDown(true) // races the first poll: either way the agent must end up connected
	time.Sleep(200 * time.Millisecond)
	r.w.SetDown(false)
	r.ready()
}

func TestSimAutoCreative(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "survival", Fork: true, GamemodeWorks: true}, func(c *agent.Config) {
		c.AutoCreative = true
		c.CreativeCommand = "/gamemode creative"
	})
	r.waitFor("/gamemode creative", 3*time.Second, func() bool { return r.w.Has("cmd", "/gamemode creative") })
	if r.w.Has("say", "gamemode") {
		t.Error("the command went to chat as text")
	}
}

func TestSimIgnoresOthersTalk(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true}, nil)
	r.w.AddPlayer("Alex", 5, 0)
	r.ready()
	r.w.Say("Alex", "Боб, иди за мной")
	time.Sleep(500 * time.Millisecond)
	if r.w.Has("cmd", "@follow") || r.w.Has("say", "") {
		t.Error("reacted to a line addressed to someone else")
	}
}

func TestSimReadsSignOnItsOwn(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10}, func(c *agent.Config) {
		c.ActivityEvery = 100 * time.Millisecond
	})
	r.w.AddSign(15, 70, 10, "Ферма тыкв")
	r.ready()
	r.waitFor("sign read aloud", 8*time.Second, func() bool {
		for _, p := range r.giga.Calls() {
			if strings.Contains(p, "Ферма тыкв") {
				return true
			}
		}
		return false
	})
}
