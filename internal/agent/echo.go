package agent

import (
	"sync"
	"time"
)

// The bot's own voice coming back through a player's open microphone. On SkyBars (21.11) 26 heard
// lines were his own words a few seconds later, and once he took one up as the player's
// ("picks up what they said"). Speech recognition garbles them ("брови врозь" came back as "брови
// в розе"), so the match is loose; tuned on that session: 18 of 26 echoes caught and no line a
// player said on their own. A line that names the bot needs a closer match: players quote him
// when they answer ("Пыхтун, а через мост на ферму идти безопасно?" after his "иди через мост").
const (
	echoAfterSpeech = 10 * time.Second // past the estimated end of his line: the other mic, recognition
	echoOverlap     = 0.5              // share of the shorter line's distinct words found in the other
	echoNamed       = 0.7              // the same when the heard line names the bot
	echoMinCommon   = 3                // distinct words in common at least
	echoKept        = 8
)

type spokenLine struct {
	text string
	at   time.Time // queued for the voice
	end  time.Time // estimated end of playback (pacing.go)
}

// spokenLog: written by the reply worker, read where heard speech comes in.
type spokenLog struct {
	mu    sync.Mutex
	lines []spokenLine
}

func (a *Agent) rememberSpoken(text string, at, end time.Time) {
	a.spoken.mu.Lock()
	defer a.spoken.mu.Unlock()
	a.spoken.lines = append(a.spoken.lines, spokenLine{text, at, end})
	if len(a.spoken.lines) > echoKept {
		a.spoken.lines = a.spoken.lines[len(a.spoken.lines)-echoKept:]
	}
}

func distinctWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range words(s) {
		out[w] = true
	}
	return out
}

// ownEcho: heard text that is most likely the bot's own recent line.
func (a *Agent) ownEcho(heard string, now time.Time) bool {
	hw := distinctWords(heard)
	if len(hw) < echoMinCommon {
		return false
	}
	need := echoOverlap
	if a.mentioned(heard) {
		need = echoNamed
	}
	a.spoken.mu.Lock()
	defer a.spoken.mu.Unlock()
	for _, s := range a.spoken.lines {
		if now.Before(s.at) || now.After(s.end.Add(echoAfterSpeech)) {
			continue
		}
		mine := distinctWords(s.text)
		common := 0
		for w := range hw {
			if mine[w] {
				common++
			}
		}
		base := min(len(hw), len(mine))
		if need == echoNamed {
			base = len(hw) // "Пыхтун, через реку есть мост?" after his "Мост через реку": 3 of 5 is a question
		}
		if common >= echoMinCommon && float64(common) >= need*float64(base) {
			return true
		}
	}
	return false
}
