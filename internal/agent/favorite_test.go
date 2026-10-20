package agent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/sim"
)

// promptHas: some chat call to the model mentioned s.
func (r *run) promptHas(s string) bool {
	for _, p := range r.giga.Calls() {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

func TestSimFavoriteStoneInCreative(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "creative", Fork: true, FreeSlots: 10}, nil)
	r.w.AddPlayer("Alex", 5, 0)
	r.ready()
	r.waitFor("favourite set and held", 3*time.Second, func() bool {
		return r.w.Has("tool", `setFavoriteItem {"hold":true,"item":"obsidian"}`)
	})
	ev := r.w.Events()
	give, set := -1, -1
	for i, e := range ev {
		if e.Kind == "tool" && e.Text == `creativeGive {"count":1,"item":"obsidian"}` && give < 0 {
			give = i
		}
		if e.Kind == "tool" && strings.HasPrefix(e.Text, "setFavoriteItem") && set < 0 {
			set = i
		}
	}
	if give < 0 || give > set {
		t.Fatalf("want one obsidian from the creative tab before setFavoriteItem, got give=%d set=%d", give, set)
	}
	r.w.Say("Alex", "Пыхтун, привет")
	r.waitFor("a reply", 3*time.Second, func() bool { return len(r.giga.Calls()) > 0 })
	if r.promptHas("любимый камешек") {
		t.Fatal("the stone on a plain hello: it opened most lines with it")
	}
	r.w.Say("Alex", "Пыхтун, что у тебя в руке?")
	r.waitFor("the stone when asked", 3*time.Second, func() bool { return r.promptHas("любимый камешек") })
}

func TestSimFavoriteStoneSurvivalOnlyWhenCarried(t *testing.T) {
	r := startSim(t, sim.Options{Mode: "survival", Fork: true}, func(c *agent.Config) { c.Creative = false })
	r.w.AddPlayer("Alex", 5, 0)
	r.ready()
	r.waitFor("favourite set", 3*time.Second, func() bool { return r.w.Has("tool", "setFavoriteItem") })
	r.w.Say("Alex", "Пыхтун, привет")
	r.waitFor("a reply", 3*time.Second, func() bool { return len(r.giga.Calls()) > 0 })
	if r.w.Has("tool", "creativeGive") {
		t.Error("creativeGive in survival")
	}
	if r.promptHas("любимый камешек") {
		t.Error("talks about a stone it does not carry")
	}
}
