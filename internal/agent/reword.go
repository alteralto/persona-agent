package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/alteralto/persona-agent/internal/giga"
)

// repeatNearVerbatim: the share of words two lines must have in common to sound like the same
// line said twice. To the same player, the stricter repeatsPublicAside applies (topics too).
const repeatNearVerbatim = 0.75

// extraAskTimeout bounds each extra model call (a reword, a shorter line). The reply worker waits
// on it, so it is kept well under the main call's 20 s.
const extraAskTimeout = 6 * time.Second

// repeatsFor: would this line sound like a stuck record? To the same player as last time (or as an
// aside) any close repeat or a repeated topic counts; to someone else only a near-verbatim line
// with the same polarity ("Иди через мост" and "Не иди через мост" share every long word).
func (a *Agent) repeatsFor(job replyJob, text string) bool {
	if job.ambient || strings.EqualFold(job.nick, a.lastAnswered) {
		return repeatsPublicAside(text, a.ownLines)
	}
	return slices.ContainsFunc(a.ownLines, func(prev string) bool {
		return overlap(text, prev) >= repeatNearVerbatim && negated(text) == negated(prev)
	})
}

// negated: the line says "не" or "нет" somewhere (words() drops words this short).
func negated(text string) bool {
	return slices.ContainsFunc(strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }),
		func(w string) bool { return w == "не" || w == "нет" || w == "ни" })
}

// extraAskAllowed: another model call only when nobody is waiting for an answer; in a busy crowd
// a slightly worse line now beats a better one after the next players' questions went stale.
func (a *Agent) extraAskAllowed() bool { return len(a.directReplies) == 0 }

const repeatFix = "\n\n(Ты только что уже сказал: «%s». Ответь по-другому: другими словами и не про то же самое.)"

// sayDifferently asks once more for a line that is not the one just said, and shapes it as the
// first one was shaped. ok is false when the model fails or the new line is empty or unsayable.
// allowShorter is false when the answer already cost one extra call: two at most per answer.
func (a *Agent) sayDifferently(ctx context.Context, llm *giga.Client, note []giga.Message, said string, job replyJob, allowShorter bool) (string, bool) {
	if len(note) == 0 || !a.extraAskAllowed() {
		return "", false
	}
	again := slices.Clone(note)
	again[len(again)-1].Content += fmt.Sprintf(repeatFix, said)
	cctx, cancel := context.WithTimeout(ctx, extraAskTimeout)
	raw, err := llm.Chat(cctx, again, 120)
	cancel()
	if err != nil {
		return "", false
	}
	text, ok := a.shapePublic(ctx, llm, raw, job, allowShorter)
	if !ok || job.prompt == "" && !moving(job.action) && promisesMove(text) {
		return "", false
	}
	return text, true
}

// shapePublic runs a model line through the checks and trims the main path uses on a public
// server: unsayable lines out, book words out, one shortening try, whole sentences, no home talk
// unless asked about it. ok is false when nothing sayable is left.
func (a *Agent) shapePublic(ctx context.Context, llm *giga.Client, raw string, job replyJob, allowShorter bool) (string, bool) {
	text := cleanReply(raw)
	if text == "" || modelRefusal(text) || unfitForHim(text) {
		return "", false
	}
	if a.cfg.PublicServer {
		text = withoutBookWords(text)
		if allowShorter && cutsMidSentence(text, publicMaxRunes) {
			if short, ok := a.sayShorter(ctx, llm, raw); ok {
				text = short
			}
		}
		text = firstSentences(text, publicMaxRunes)
		if !hasTopicWord(job.text, teaWord) && !hasTopicWord(job.text, burrowWord) {
			if rest := withoutHomeTalk(text); rest != "" {
				text = rest
			}
		}
	}
	if strings.TrimSpace(chatReply(text)) == "" {
		return "", false
	}
	return text, true
}

const shorterAsk = "Скажи то же самое короче, одной фразой до 70 символов, в том же характере, без кавычек: «%s»"

// sayShorter asks once for a shorter version of a line that would otherwise break off mid-sentence.
// It gets the model's whole line, not the one already trimmed to 120 runes.
func (a *Agent) sayShorter(ctx context.Context, llm *giga.Client, long string) (string, bool) {
	if !a.extraAskAllowed() {
		return "", false
	}
	cctx, cancel := context.WithTimeout(ctx, extraAskTimeout)
	text, err := llm.Chat(cctx, []giga.Message{{Role: "user", Content: fmt.Sprintf(shorterAsk, strings.TrimSpace(long))}}, 80)
	cancel()
	if err != nil {
		return "", false
	}
	text = withoutBookWords(cleanReply(text))
	if strings.TrimSpace(text) == "" || modelRefusal(text) || unfitForHim(text) || cutsMidSentence(text, publicMaxRunes) {
		return "", false
	}
	return text, true
}
