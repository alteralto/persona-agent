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
	case ActFollow, ActLead, ActSupply, ActFly, ActFlyTo:
		return true
	}
	return false
}

const standStillFix = "\n\nВАЖНО: ты сейчас стоишь на месте и никуда не идёшь. Не пиши «иду», «пойдём», " +
	"«следуй за мной», «покажу дорогу» и подобного. Ответь словами, без обещания движения."
