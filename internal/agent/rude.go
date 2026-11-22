package agent

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Rude players on a public server. On SkyBars (21.11) one player said "ты жопа" some fifty times
// in 12 minutes and got 59 answers: the line opens with "ты", so the nearby-talk rules took it as
// said to the bot, and the bot kept the troll's show going. Now an insult said to the bot gets one
// answer in character, the next ones wait, and a player who keeps it up is not answered at all for
// a while. Only lines that reach the bot count: insults between players are none of its business.
const (
	rudeWindow     = 5 * time.Minute // one answer to rudeness per player in this long
	rudeMuteAfter  = 3               // insults within rudeWindow that mute the player
	rudeMuteFor    = 5 * time.Minute
	rudeRemembered = 200 // players tracked at once; expired ones are dropped first
)

// insultStems match the start of a word, so "страхуйся" or "оскорбление" do not count. Only
// insults: the swearing kids pad their talk with ("блять", "сука", "нахуй" - over 100 times on
// 21.11, often at the bot: "Агент, ты тут? Бля") mutes nobody.
var insultStems = []string{"жоп", "дебил", "идиот", "заткн", "говноед", "мраз", "долбо", "тупиц", "шлюх",
	"гандон", "гондон", "пидо", "пидр", "пиздобол"}

// insultWords are whole words: as stems they would catch "лохматый", "дурацкий" (his own favourite
// word), "уродилась", "чмокнуть", "даунхилл".
var insultWords = []string{"лох", "лошара", "дурак", "дура", "тупой", "тупая", "урод", "урода", "уроды", "уродина",
	"уродец", "чмо", "чмошник", "чмошница", "даун", "дауны"}

// swearStems and swearWords: swearing that is no insult; never in the bot's own mouth.
var swearStems = []string{"хуй", "хуе", "хуё", "хуя", "пизд", "бля", "ебан", "ёбан", "ебал", "ебат", "ебу",
	"еби", "уеб", "уёб", "заеб", "заёб", "нахуй", "охуе", "охуи"}
var swearWords = []string{"сука", "суки", "сучка", "сучара", "ёб", "еб"}

// letterWords splits into lower-case words, short ones too (words() drops "ёб", "не").
func letterWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) })
}

func rude(text string) bool {
	for _, w := range letterWords(text) {
		if slices.Contains(insultWords, w) || slices.ContainsFunc(insultStems, func(s string) bool { return strings.HasPrefix(w, s) }) {
			return true
		}
	}
	return false
}

// unfitForHim: a line the character must not say, insults and swearing alike. On 21.11 he told
// the troll "Жопа так жопа, нечего тут обижаться".
func unfitForHim(text string) bool {
	if rude(text) {
		return true
	}
	for _, w := range letterWords(text) {
		if slices.Contains(swearWords, w) || slices.ContainsFunc(swearStems, func(s string) bool { return strings.HasPrefix(w, s) }) {
			return true
		}
	}
	return false
}

type rudeTrack struct {
	lines      []time.Time // insults within rudeWindow
	answered   time.Time   // the last insult let through to be answered
	mutedUntil time.Time
}

// muted: the player is not answered now.
func (a *Agent) muted(nick string, now time.Time) bool {
	tr := a.rudeness[strings.ToLower(nick)]
	return tr != nil && now.Before(tr.mutedUntil)
}

// rudeGate says whether a line is left unanswered. said: the line was put to the bot (by name, or
// aloud next to it); only such insults count. A muted player is not answered even when naming the
// bot ("Тихо, пыхтун"; handleMessage drops their lines earlier still, a plain stop aside); the rest
// of anyone's lines are judged as usual.
func (a *Agent) rudeGate(nick, text string, now time.Time, said bool) (skip bool, why string) {
	if a.muted(nick, now) {
		return true, "rude player muted"
	}
	if !said || !rude(text) {
		return false, ""
	}
	if a.rudeness == nil {
		a.rudeness = map[string]*rudeTrack{}
	}
	if len(a.rudeness) >= rudeRemembered {
		for k, tr := range a.rudeness {
			if !now.Before(tr.mutedUntil) && (len(tr.lines) == 0 || now.Sub(tr.lines[len(tr.lines)-1]) >= rudeWindow) {
				delete(a.rudeness, k)
			}
		}
	}
	key := strings.ToLower(nick)
	tr := a.rudeness[key]
	if tr == nil {
		if len(a.rudeness) >= rudeRemembered {
			return false, "" // full of fresh entries: this one goes untracked rather than evict a mute
		}
		tr = &rudeTrack{}
		a.rudeness[key] = tr
	}
	recent := tr.lines[:0]
	for _, t := range tr.lines {
		if now.Sub(t) < rudeWindow {
			recent = append(recent, t)
		}
	}
	tr.lines = append(recent, now)
	if len(tr.lines) >= rudeMuteAfter {
		tr.mutedUntil = now.Add(rudeMuteFor)
		return true, "rude player muted"
	}
	if !tr.answered.IsZero() && now.Sub(tr.answered) < rudeWindow {
		return true, "rudeness already answered"
	}
	tr.answered = now
	return false, ""
}

// speakerNear: the player is within voice range (nearby.go looks round at most every 2 s).
func (a *Agent) speakerNear(ctx context.Context, nick string, now time.Time) bool {
	if !a.lookRound(ctx, now) {
		return false
	}
	d, ok := a.chatDist[strings.ToLower(nick)]
	return ok && d <= nearbyVoiceRadius
}
