// Package memory keeps one file per person the character talks to: dated events and a short
// set of notes, after the agent-workplace layout (one entity, one file, readable in Obsidian).
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	notesHeader  = "## Заметки"
	eventsHeader = "## События"
)

type Store struct {
	dir string
	mu  sync.Mutex
}

func New(dir string) *Store { return &Store{dir: dir} }

var unsafe = regexp.MustCompile(`[^A-Za-z0-9_\-]`)

func (s *Store) path(nick string) string {
	return filepath.Join(s.dir, unsafe.ReplaceAllString(nick, "_")+".md")
}

type person struct {
	firstSeen string
	notes     []string
	events    []string
}

func (s *Store) load(nick string) person {
	raw, err := os.ReadFile(s.path(nick))
	if err != nil {
		return person{}
	}
	var p person
	section := ""
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "first_seen: "):
			p.firstSeen = strings.TrimPrefix(line, "first_seen: ")
		case line == notesHeader:
			section = "notes"
		case line == eventsHeader:
			section = "events"
		case strings.HasPrefix(line, "- "):
			if section == "notes" {
				p.notes = append(p.notes, strings.TrimPrefix(line, "- "))
			} else if section == "events" {
				p.events = append(p.events, strings.TrimPrefix(line, "- "))
			}
		}
	}
	return p
}

func (s *Store) save(nick string, p person) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nfirst_seen: %s\nlast_seen: %s\n\n%s\n", nick, p.firstSeen,
		time.Now().Format("2006-01-02 15:04"), notesHeader)
	for _, n := range p.notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	fmt.Fprintf(&b, "\n%s\n", eventsHeader)
	for _, e := range p.events {
		fmt.Fprintf(&b, "- %s\n", e)
	}
	tmp := s.path(nick) + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(nick))
}

// maxEvents keeps a file readable; older events have been folded into the notes by then.
const maxEvents = 60

// Append records one exchange: what the player said and what the character answered.
func (s *Store) Append(nick, heard, said string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.load(nick)
	if p.firstSeen == "" {
		p.firstSeen = time.Now().Format("2006-01-02 15:04")
	}
	e := fmt.Sprintf("%s — %s: «%s»", time.Now().Format("2006-01-02 15:04"), nick, oneLine(heard))
	if said != "" {
		e += fmt.Sprintf(" — Пыхтун: «%s»", oneLine(said))
	}
	p.events = append(p.events, e)
	if len(p.events) > maxEvents {
		p.events = p.events[len(p.events)-maxEvents:]
	}
	return s.save(nick, p)
}

// Context is what the reply prompt gets about a person: notes and the last few events.
func (s *Store) Context(nick string, lastEvents int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.load(nick)
	if len(p.notes) == 0 && len(p.events) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Что ты помнишь о %s (знаком с %s):\n", nick, p.firstSeen)
	for _, n := range p.notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	ev := p.events
	if len(ev) > lastEvents {
		ev = ev[len(ev)-lastEvents:]
	}
	for _, e := range ev {
		fmt.Fprintf(&b, "- %s\n", e)
	}
	return b.String()
}

// Pending lists people whose events outnumber what the notes were built from.
func (s *Store) Pending(minEvents int) []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		if e.IsDir() || name == e.Name() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		text := string(raw)
		marker := "summarized_events: "
		done := 0
		if i := strings.Index(text, marker); i >= 0 {
			fmt.Sscanf(text[i+len(marker):], "%d", &done)
		}
		if strings.Count(text, "\n- ")-done >= minEvents {
			out = append(out, name)
		}
	}
	return out
}

// Events returns all recorded events for summarising.
func (s *Store) Events(nick string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(nick).events
}

// SetNotes replaces the notes with a fresh summary.
func (s *Store) SetNotes(nick string, notes []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.load(nick)
	p.notes = notes
	if err := s.save(nick, p); err != nil {
		return err
	}
	// Remember how many lines the summary covered, so Pending does not redo it.
	path := s.path(nick)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(raw)
	count := strings.Count(text, "\n- ")
	text = strings.Replace(text, "\n\n"+notesHeader, fmt.Sprintf("\nsummarized_events: %d\n\n%s", count, notesHeader), 1)
	return os.WriteFile(path, []byte(text), 0o644)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}
