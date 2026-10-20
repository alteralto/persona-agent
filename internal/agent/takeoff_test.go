package agent

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestTakeoffAnnouncedOnceInAWhile(t *testing.T) {
	a := &Agent{log: slog.New(slog.NewTextHandler(io.Discard, nil)), replyQueue: make(chan replyJob, 1)}
	a.announceTakeoff("взлетаешь", nil)
	select {
	case job := <-a.replyQueue:
		if !job.takeoff {
			t.Fatal("a take-off line not marked as one")
		}
	default:
		t.Fatal("the first take-off said nothing")
	}
	// The next flight a minute and a half later: in the air without a word.
	a.lastTakeoffSay = time.Now().Add(-90 * time.Second)
	a.announceTakeoff("взлетаешь", nil)
	if len(a.replyQueue) != 0 {
		t.Fatal("announced again 90 s after the last one")
	}
	a.lastTakeoffSay = time.Now().Add(-takeoffSayGap - time.Second)
	a.announceTakeoff("взлетаешь", nil)
	if len(a.replyQueue) != 1 {
		t.Fatal("silent after the gap was over")
	}
}

func TestTakeoffNoteShowsEarlierLines(t *testing.T) {
	if takeoffNote(nil) != "" {
		t.Fatal("a note with nothing said yet")
	}
	n := takeoffNote([]string{"Опять эти крылья! Держись крепче.", "Ох, снова вверх..."})
	for _, want := range []string{"Опять эти крылья! Держись крепче.", "Ох, снова вверх...", "иначе"} {
		if !strings.Contains(n, want) {
			t.Fatalf("note lacks %q: %s", want, n)
		}
	}
}
