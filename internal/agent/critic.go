package agent

import "strings"

// moveWords are promises of movement. Said while the bot stands still they are a lie the
// players act on ("follow me!" and nobody moves).
var moveWords = map[string]bool{
	"иду": true, "идём": true, "идем": true, "пойдём": true, "пойдем": true, "пошли": true,
	"топаю": true, "лезу": true, "бегу": true, "веду": true, "приду": true, "следуй": true,
	"шагаю": true, "отведу": true, "провожу": true,
}

// promisesMove reports whether a reply claims the bot is moving or leading.
func promisesMove(text string) bool {
	for _, w := range words(text) {
		if moveWords[w] {
			return true
		}
	}
	return strings.Contains(strings.ToLower(text), "за мной")
}

// moving reports whether the action really moves the bot.
func moving(action string) bool {
	switch action {
	case ActFollow, ActLead, ActSupply, ActFly, ActFlyTo, ActFlyLow, ActFlyTricks, ActFlyRoute:
		return true
	}
	return false
}

const standStillFix = "\n\nВАЖНО: ты сейчас стоишь на месте и никуда не идёшь. Не пиши «иду», «пойдём», " +
	"«следуй за мной», «покажу дорогу» и подобного. Ответь словами, без обещания движения."

// placeStems are the stems of a place's own words (four letters and more, cut to four, ё as е, so
// "жёлтое" also catches "желтая"):
// what a riddle about the place must not say. "Найдите жёлтое кольцо восточнее отсюда" was the
// riddle about "жёлтое кольцо" on the test bench (01.11) -- the prompt's "do not repeat the
// description" was not enough.
func placeStems(desc string) []string {
	var out []string
	for _, w := range words(desc) {
		r := []rune(strings.ReplaceAll(w, "ё", "е"))
		if len(r) < 4 {
			continue
		}
		r = r[:4]
		out = append(out, string(r))
	}
	return out
}

// namesPlace reports the first word of the reply that starts with one of the stems.
func namesPlace(text string, stems []string) (string, bool) {
	for _, w := range words(text) {
		n := strings.ReplaceAll(w, "ё", "е")
		for _, s := range stems {
			if strings.HasPrefix(n, s) {
				return w, true
			}
		}
	}
	return "", false
}

// riddleFix is the rewrite asked for when a riddle names its place. Asked to hint "at what it looks
// like and what is near", it made a red ring on posts in an empty field "a stone round thing among
// trees": nothing is to be made up.
const riddleFix = "\n\nВАЖНО: ты назвал место прямо (слово «%s»). Перепиши загадку: не называй сам предмет, " +
	"намекни на его форму, цвет или назначение иносказательно. Не выдумывай того, чего нет в описании места."
