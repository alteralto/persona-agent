package agent

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/laya"
)

// journalEntry is one decision, kept for the self-improvement loop: new lines for the bench,
// and signals that a decision was wrong.
type journalEntry struct {
	At      time.Time `json:"at"`
	Nick    string    `json:"nick"`
	Source  string    `json:"source"` // chat | voice
	Text    string    `json:"text"`
	RU      string    `json:"ru"`
	RUProb  float64   `json:"ru_prob"`
	EN      string    `json:"en"`
	ENProb  float64   `json:"en_prob"`
	Rule    bool      `json:"rule"`
	Action  string    `json:"action"`
	Reply   bool      `json:"reply"`
	FromBot bool      `json:"from_bot"`
	// Repeat: the same player asked again within 30 s with overlapping words, a hint that the
	// previous decision missed what they wanted.
	Repeat bool `json:"repeat,omitempty"`
}

type journal struct {
	path string
	last map[string]journalEntry // by nick
}

func newJournal(path string) *journal {
	return &journal{path: path, last: map[string]journalEntry{}}
}

func (j *journal) record(nick, source, text string, answers map[string]laya.Answer, d Decision, byRule, fromBot bool) {
	if j == nil || j.path == "" {
		return
	}
	ru, en := answers["act_ru"], answers["act_en"]
	e := journalEntry{
		At: time.Now(), Nick: nick, Source: source, Text: text,
		RU: ru.Choice, RUProb: round(ru.Probabilities[ru.Choice]),
		EN: en.Choice, ENProb: round(en.Probabilities[en.Choice]),
		Rule: byRule, Action: d.Action, Reply: d.Reply, FromBot: fromBot,
	}
	if prev, ok := j.last[nick]; ok && e.At.Sub(prev.At) < 30*time.Second && overlap(prev.Text, text) >= 0.4 {
		e.Repeat = true
	}
	j.last[nick] = e
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	raw, _ := json.Marshal(e)
	_, _ = f.Write(append(raw, '\n'))
}

// overlap is the share of the shorter line's words that also appear in the other.
func overlap(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	if len(wa) > len(wb) {
		wa, wb = wb, wa
	}
	set := map[string]bool{}
	for _, w := range wb {
		set[w] = true
	}
	n := 0
	for _, w := range wa {
		if set[w] {
			n++
		}
	}
	return float64(n) / float64(len(wa))
}

func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'а' && r <= 'я' || r == 'ё' || r >= '0' && r <= '9')
	}) {
		if len([]rune(w)) > 2 {
			out = append(out, w)
		}
	}
	return out
}
