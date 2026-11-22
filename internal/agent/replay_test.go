//go:build replay

package agent_test

// A replay of real voice lines from a public server through the simulated world, in real time.
// Not part of the normal run (build tag "replay"): it takes as long as the lines do.
//
//	REPLAY_LINES=lines.json REPLAY_OUT=out.json go test -tags replay -run TestReplay -timeout 30m ./internal/agent
//
// lines.json: [{"t": seconds from start, "nick": "...", "text": "..."}]. Every speaker stands a few
// blocks from the bot. The out file lists what the bot said and which lines reached the model.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/sim"
	"github.com/alteralto/persona-agent/internal/voice"
)

func TestReplay(t *testing.T) {
	path := os.Getenv("REPLAY_LINES")
	if path == "" {
		t.Skip("REPLAY_LINES not set")
	}
	var lines []struct {
		T    float64 `json:"t"`
		Nick string  `json:"nick"`
		Text string  `json:"text"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &lines); err != nil {
		t.Fatal(err)
	}
	r := startSim(t, sim.Options{Mode: "survival", Fork: true}, func(c *agent.Config) {
		c.PublicServer = true
		c.ReplyEvery = 1500 * time.Millisecond // as cmd/agent now; the old code had 5 s
		if v := os.Getenv("REPLAY_REPLY_EVERY"); v != "" {
			c.ReplyEvery, _ = time.ParseDuration(v)
		}
	})
	// A real model rarely says the same thing twice; this one answers with the player's words.
	var n atomic.Int32
	r.giga.SetReply(func(prompt string) string {
		i := n.Add(1)
		said := ""
		for _, l := range strings.Split(prompt, "\n") {
			if _, s, ok := strings.Cut(l, ": "); ok && strings.HasPrefix(l, "Игрок ") {
				said = s
			}
		}
		w := strings.Fields(said)
		return fmt.Sprintf("Ответ %d про %s", i, strings.Join(w[:min(4, len(w))], " "))
	})
	seen := map[string]bool{}
	for i, l := range lines {
		if !seen[l.Nick] {
			seen[l.Nick] = true
			r.w.AddPlayer(l.Nick, float64(3+i%5), float64(2+len(seen)%4))
		}
	}
	r.ready()
	start := time.Now()
	for _, l := range lines {
		if d := time.Duration(l.T*float64(time.Second)) - time.Since(start); d > 0 {
			time.Sleep(d)
		}
		r.heard <- voice.Heard{Nick: l.Nick, Text: l.Text}
	}
	time.Sleep(15 * time.Second)
	var says []string
	for _, e := range r.w.Events() {
		if e.Kind == "say" {
			says = append(says, fmt.Sprintf("%6.1f %s", e.At.Seconds(), e.Text))
		}
	}
	out := map[string]any{"lines": len(lines), "says": says, "prompts": r.giga.Calls(), "log": r.log.String()}
	if p := os.Getenv("REPLAY_OUT"); p != "" {
		b, _ := json.MarshalIndent(out, "", " ")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d lines heard, %d said", len(lines), len(says))
}
