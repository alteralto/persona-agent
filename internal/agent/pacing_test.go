package agent

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestSpeechEnd(t *testing.T) {
	now := time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC)
	line := "Брови в порядке, ракетами не балуюсь." // 2.0 s of audio on 21.11
	end := speechEnd(time.Time{}, now, line)
	if d := end.Sub(now); d < 2*time.Second || d > 3*time.Second {
		t.Fatalf("one line: %v", d)
	}
	// A second line queued right away plays after the first.
	end2 := speechEnd(end, now, line)
	if d := end2.Sub(end); d < 2*time.Second || d > 3*time.Second {
		t.Fatalf("queued line: %v", d)
	}
}

func TestReplyWait(t *testing.T) {
	now := time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC)
	a := &Agent{cfg: Config{ReplyEvery: 1500 * time.Millisecond}}
	a.lastReply = now.Add(-time.Second)
	if w := a.replyWait(now); w != 500*time.Millisecond {
		t.Fatalf("text chat floor: %v", w)
	}
	a.lastReply = now.Add(-time.Minute)
	if w := a.replyWait(now); w > 0 {
		t.Fatalf("idle bot waits %v", w)
	}
}

func TestAsksSomething(t *testing.T) {
	for in, want := range map[string]bool{
		"Ммм, как тебя зовут":   true,
		"А где тут ферма":       true,
		"ты кто?":               true,
		"можешь поменять брови": true,
		"Ты жопа":               false,
		"Ладно, пойду":          false,
		"Поехали!":              false,
	} {
		if got := asksSomething(in); got != want {
			t.Errorf("asksSomething(%q) = %v", in, got)
		}
	}
}

func TestSaysYou(t *testing.T) {
	for in, want := range map[string]bool{
		"А ты знаешь, где тут ферма": true,
		"можешь поменять брови":      true,
		"Как попасть на ферму?":      false,
		"Ладно, пойду":               false,
	} {
		if got := saysYou(in); got != want {
			t.Errorf("saysYou(%q) = %v", in, got)
		}
	}
}

// A full answer queue must not stop the loop that takes in voice, chat and movement (review).
func TestQueueDirectNeverBlocks(t *testing.T) {
	a := &Agent{directReplies: make(chan replyJob, 2), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan struct{})
	go func() {
		for _, n := range []string{"a", "b", "c"} {
			a.queueDirect(replyJob{nick: n})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queueDirect blocked on a full queue")
	}
	if got := (<-a.directReplies).nick + (<-a.directReplies).nick; got != "bc" {
		t.Fatalf("kept %q, want the newest two", got)
	}
}
