package agent

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
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

var teaWord = regexp.MustCompile(`^(ча[йяюе]|чаем|ча[её]к|чайку|чайком|чайочк[а-я]*|чайник[а-я]*|чаепити[а-я]*)$`)
var burrowWord = regexp.MustCompile(`^нор(а|ы|е|у|ой|ою|ке|ка|ку|ки|очк[а-я]*)$`)

// withoutHomeTalk drops the sentences about tea and the burrow, which strangers on a public
// server did not ask about ("опять куда-то делась моя любимая кружка чая", 21.11). "" when
// nothing else is left.
func withoutHomeTalk(text string) string {
	var kept []string
	start := 0
	runes := []rune(text)
	for i, r := range runes {
		end := i == len(runes)-1
		if r == '.' || r == '!' || r == '?' || r == '…' || end {
			if !end && i+1 < len(runes) && runes[i+1] != ' ' {
				continue // "..." or "?!": the sentence goes on to the space
			}
			s := strings.TrimSpace(string(runes[start : i+1]))
			start = i + 1
			if s != "" && !hasTopicWord(s, teaWord) && !hasTopicWord(s, burrowWord) {
				kept = append(kept, s)
			}
		}
	}
	return strings.Join(kept, " ")
}

func hasTopicWord(text string, pattern *regexp.Regexp) bool {
	for _, word := range words(text) {
		if pattern.MatchString(word) {
			return true
		}
	}
	return false
}

// Only spontaneous public chatter may be skipped. Player questions and game clues still get their turn.
func repeatsPublicAside(text string, recent []string) bool {
	tea, burrow := hasTopicWord(text, teaWord), hasTopicWord(text, burrowWord)
	for _, prev := range recent {
		if overlap(text, prev) >= 0.7 || tea && hasTopicWord(prev, teaWord) || burrow && hasTopicWord(prev, burrowWord) {
			return true
		}
	}
	return false
}

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

// withoutLeadingNick drops a Latin nick that opens a line before it is spoken: the chat shows
// "NAPOLION6784, ...", but read aloud letter by letter it was eight seconds of noise (21.11).
func withoutLeadingNick(text string) string {
	head, rest, ok := strings.Cut(text, ",")
	head = strings.TrimSpace(head)
	if !ok || head == "" || strings.ContainsRune(head, ' ') || !strings.ContainsFunc(head, func(r rune) bool {
		return r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}) {
		return text
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return text
	}
	r := []rune(rest)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// modelTalk: "языковая/языковые модель/модели", "генеративные (языковые) модели", "не обладает /
// не обладают собственным мнением" - the words next to each other, so "для языкового пакета модели
// мобов" is no refusal.
var modelTalk = regexp.MustCompile(`(языков|генеративн)\S*\s+(языков\S*\s+)?модел|не\s+облада\S*\s+собственн\S*\s+мнени`)

// modelRefusal: the model talking about itself instead of the character answering.
func modelRefusal(text string) bool {
	t := strings.ToLower(text)
	// Stems, not phrases: on SkyBars (21.11) "генеративные языковые модели не обладают собственным
	// мнением" and "к сожалению, иногда генеративные языковые модели могут создавать" were said
	// aloud 11 times, the singular-only list never matched them.
	if modelTalk.MatchString(t) {
		return true
	}
	for _, w := range []string{"gigachat", "гигачат", "нейросет", "искусственный интеллект",
		"не могу обсуждать", "не могу ответить на этот вопрос", "давайте поговорим о чём-нибудь ещё",
		"как ии", "я ии", "чат-бот"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// refusalStandIns: what the character says instead of a refusal.
var refusalStandIns = []string{"не, это не ко мне", "я по фейерверкам, тут пас", "давай про что-нибудь другое", "хз, я хоббит"}

// bookWords: forms of address straight out of a fairy tale that the prompt alone did not keep out
// of the public chat ("bellin, дружище, выбери...", "ох, бедняга Макс", 21.11).
var bookWords = []string{"дружище", "друг мой", "соседушка", "сосед", "бедняга", "приветствую"}

// withoutBookWords drops them as whole words, with a comma that set them off.
func withoutBookWords(text string) string {
	r := []rune(text)
	lower := []rune(strings.ToLower(text))
	isLetter := func(i int) bool { return i >= 0 && i < len(r) && unicode.IsLetter(r[i]) }
	var out []rune
	for i := 0; i < len(r); {
		matched := 0
		if !isLetter(i - 1) {
			for _, w := range bookWords {
				wr := []rune(w)
				if i+len(wr) <= len(r) && string(lower[i:i+len(wr)]) == w && !isLetter(i+len(wr)) {
					matched = len(wr)
					break
				}
			}
		}
		if matched == 0 {
			out = append(out, r[i])
			i++
			continue
		}
		i += matched
		// drop a comma right after it, or else the comma (and space) before it
		j := i
		for j < len(r) && r[j] == ' ' {
			j++
		}
		if j < len(r) && r[j] == ',' {
			i = j + 1
		} else {
			for len(out) > 0 && (out[len(out)-1] == ' ' || out[len(out)-1] == ',') {
				out = out[:len(out)-1]
			}
		}
	}
	res := strings.Join(strings.Fields(string(out)), " ")
	return strings.TrimLeft(res, ", ")
}

const (
	ownLinesKept = 12               // the bot's own recent lines: shown to the model, checked for repeats
	topicRest    = 10 * time.Minute // a public stock topic is not used again sooner
)
