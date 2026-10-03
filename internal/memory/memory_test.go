package memory

import (
	"strings"
	"testing"
)

func TestAppendContextAndNotes(t *testing.T) {
	s := New(t.TempDir())
	if s.Context("Pon4ikYT", 3) != "" {
		t.Fatal("unknown person should have no context")
	}
	for i := 0; i < 5; i++ {
		if err := s.Append("Pon4ikYT", "Пыхтун, подойди с чаем", "Иду, иду!"); err != nil {
			t.Fatal(err)
		}
	}
	ctx := s.Context("Pon4ikYT", 3)
	if strings.Count(ctx, "подойди с чаем") != 3 {
		t.Errorf("want 3 last events in context, got:\n%s", ctx)
	}
	if got := s.Pending(5); len(got) != 1 || got[0] != "Pon4ikYT" {
		t.Errorf("Pending = %v", got)
	}
	if err := s.SetNotes("Pon4ikYT", []string{"часто просит чай", "осторожничает на уступах"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Pending(5); len(got) != 0 {
		t.Errorf("after summary nothing should be pending, got %v", got)
	}
	ctx = s.Context("Pon4ikYT", 1)
	if !strings.Contains(ctx, "часто просит чай") || len(s.Events("Pon4ikYT")) != 5 {
		t.Errorf("notes or events lost:\n%s", ctx)
	}
}

func TestUnsafeNick(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Append("../evil nick", "hi", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.path("../evil nick"), "___evil_nick.md") {
		t.Errorf("nick not sanitized: %s", s.path("../evil nick"))
	}
}
