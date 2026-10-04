package agent

import "testing"

func TestReplyQuality(t *testing.T) {
	q := ReplyQuality([]string{
		"Наконец-то добралась! Входи скорее, девочка, тут тепло.",
		"Ох, опять ракеты нужны магу, пойду за порохом.",
		"Ох, опять ракеты нужны магу, пойду за бумагой.",
		"И тебе не хворать! Заходи на чай.",
	})
	if q.Lines != 4 || q.Gendered != 1 || q.Repeats != 1 {
		t.Errorf("got %+v", q)
	}
	if g := ReplyQuality([]string{"Ты чего там высматриваешь?", "Стою-стою, никуда не денусь!"}); g.Gendered != 0 {
		t.Errorf("neutral lines flagged: %+v", g)
	}
}
