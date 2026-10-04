package agent

import (
	"fmt"
	"regexp"
)

// Quality sums up a batch of the bot's lines against the persona's rules: short, varied, no
// guessing at the listener's gender. The nightly simulated scene prints it, so a prompt change
// that makes the bot chattier or repetitive shows up in the morning report.
type Quality struct {
	Lines    int
	AvgRunes int
	Over90   int // the prompt asks for 90 characters
	Repeats  int // pairs of lines sharing half their words
	Gendered int // lines that address someone as a girl/boy or use gendered verbs about them
}

// gendered catches the forms the bot used live: «девочка», «добралась», «залетел».
var gendered = regexp.MustCompile(`(?i)(девочк|девушк|парень|парниш|леди|красавиц|бедняг|` +
	`(ты|вы)\s+(уже\s+)?(при|до|за|у)?(шл[аи]|ш[её]л|летел[аи]?|бралс[яь]|бралась)|добралась|пришла|дошла|залетела)`)

func ReplyQuality(lines []string) Quality {
	q := Quality{Lines: len(lines)}
	total := 0
	for i, l := range lines {
		n := len([]rune(l))
		total += n
		if n > 90 {
			q.Over90++
		}
		if gendered.MatchString(l) {
			q.Gendered++
		}
		for _, prev := range lines[:i] {
			if overlap(l, prev) >= 0.5 {
				q.Repeats++
				break
			}
		}
	}
	if q.Lines > 0 {
		q.AvgRunes = total / q.Lines
	}
	return q
}

func (q Quality) String() string {
	return fmt.Sprintf("lines %d, avg %d chars, over 90: %d, repeats: %d, gendered: %d",
		q.Lines, q.AvgRunes, q.Over90, q.Repeats, q.Gendered)
}
