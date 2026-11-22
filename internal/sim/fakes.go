package sim

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Laya answers every question with its first neutral option: act = wait, say = reply. The
// keyword rules on top of Laya then decide, which is what most live commands go through anyway.
// Point the agent at the real laya-serve to test Laya itself.
func Laya() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		answers := map[string]any{}
		for name, q := range req.Questions {
			choice := "wait"
			if _, ok := q.Criteria["reply"]; ok {
				choice = "reply"
			}
			probs := map[string]float64{}
			for k := range q.Criteria {
				probs[k] = 0.02
			}
			probs[choice] = 0.9
			answers[name] = map[string]any{"choice": choice, "confidence": 0.9, "probabilities": probs}
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"answers": answers})
	})
}

// Giga is a GigaChat stand-in: OAuth hands out a token, chat answers with a short line that
// names what the bot was told it is doing, so timelines show which prompt produced it.
type Giga struct {
	mu      sync.Mutex
	Prompts []string // the last user message of every chat call
	Delay   time.Duration
	// Reply, when set, answers instead of the canned line (tests that need distinct answers).
	Reply func(prompt string) string
}

func (g *Giga) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"access_token": "sim",
			"expires_at": time.Now().Add(30 * time.Minute).UnixMilli()})
	})
	mux.HandleFunc("/models", func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"data": []map[string]string{{"id": "GigaChat-2"}}})
	})
	mux.HandleFunc("/chat/completions", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		last := ""
		if n := len(req.Messages); n > 0 {
			last = req.Messages[n-1].Content
		}
		g.mu.Lock()
		g.Prompts = append(g.Prompts, last)
		delay, reply := g.Delay, g.Reply
		g.mu.Unlock()
		time.Sleep(delay)
		answer := cannedReply(last)
		if reply != nil {
			answer = reply(last)
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"choices": []map[string]any{
			{"message": map[string]string{"role": "assistant", "content": answer}}}})
	})
	return mux
}

// SetReply answers with f instead of the canned line.
func (g *Giga) SetReply(f func(prompt string) string) {
	g.mu.Lock()
	g.Reply = f
	g.mu.Unlock()
}

func (g *Giga) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.Prompts...)
}

// cannedReply turns the prompt into a line: "Сейчас: ты идёшь следом за игроком" becomes
// "(ты идёшь следом за игроком)"; free prompts keep their first words.
func cannedReply(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if s, ok := strings.CutPrefix(line, "Сейчас: "); ok {
			return "(" + s + ")"
		}
	}
	lines := strings.Split(strings.TrimSpace(prompt), "\n")
	r := []rune(lines[len(lines)-1])
	if len(r) > 70 {
		r = r[:70]
	}
	return "(" + string(r) + ")"
}
