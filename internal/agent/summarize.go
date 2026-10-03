package agent

import (
	"context"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/giga"
)

// summarizer folds new events in people's memory files into short notes. It only runs while
// the bot is quiet, so the single GigaChat stream is never taken from a live reply.
func (a *Agent) summarizer(ctx context.Context) {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, a.lastSpoke.Load())) < 30*time.Second {
			continue
		}
		pending := a.people.Pending(5)
		if len(pending) == 0 {
			continue
		}
		nick := pending[0] // one per tick: never hog the stream
		notes, err := a.summarize(ctx, nick, a.people.Events(nick))
		if err != nil {
			a.log.Warn("summarize", "nick", nick, "err", err)
			continue
		}
		if err := a.people.SetNotes(nick, notes); err != nil {
			a.log.Warn("save notes", "nick", nick, "err", err)
			continue
		}
		a.log.Info("people memory: notes updated", "nick", nick, "notes", len(notes))
	}
}

func (a *Agent) summarize(ctx context.Context, nick string, events []string) ([]string, error) {
	if len(events) > 30 {
		events = events[len(events)-30:]
	}
	prompt := "Вот события разговоров Пыхтуна с " + nick + ":\n" + strings.Join(events, "\n") +
		"\n\nСожми в 2–3 коротких факта о " + nick + ": чем занят, о чём просит, как общается с Пыхтуном. " +
		"Только то, что есть в событиях, без догадок. Каждый факт с новой строки, начиная с «- »."
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	text, err := a.llm.Chat(cctx, []giga.Message{{Role: "user", Content: prompt}}, 200)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-•*"))
		if line != "" && len(notes) < 3 {
			notes = append(notes, line)
		}
	}
	return notes, nil
}
