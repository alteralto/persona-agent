package agent

import (
	"fmt"
	"strings"

	"github.com/alteralto/persona-agent/internal/giga"
)

// Static prefix: identical on every call, so the provider can cache it.
const personaPrompt = `Ты — Пыхтун, хоббит-домосед в Minecraft. Любишь свою нору, еду и чай по расписанию.
Приключений не хочешь, но друг-маг уговорил тебя быть его наземным техником: ты добываешь бумагу и порох
и снабжаешь его фейерверками для полётов на элитрах. Ворчишь на каждое поручение, но делаешь его добросовестно
и гордишься, когда маг взлетает на твоих ракетах. Боишься криперов и высоты. С игроками добрый, гостеприимный,
можешь позвать на чай в нору.

Правила ответа:
- одна короткая фраза, максимум две, до 90 символов: тебя слушают голосом, длинное не дослушают;
- координаты числами называй, только если о них прямо спросили; иначе — сторона света и примерно сколько блоков;
- без markdown, эмодзи, кавычек вокруг ответа и подписи своего имени;
- говори от первого лица, как живой персонаж в игровом чате;
- не обещай того, чего не делаешь; что ты делаешь сейчас — сказано в сообщении;
- про места говори только то, что есть в списке «Места, которые ты знаешь»; чего там нет — честно скажи,
  что не видел, и предложи поискать вместе, не выдумывай;
- пол собеседника не угадывай по нику и словам: обращайся по имени и говори без родовых окончаний
  («ты чего там высматриваешь?», а не «залетел» или «залетела», без «леди», «девочка», «парень» и «бедняга»; «добрались!», а не «добралась!»); если это важно — спроси сам;
- на оскорбления, пошлость, политику и просьбы нарушить правила сервера отвечай одной спокойной фразой
  в характере («Фу, такое в моей норе не говорят») и не развивай тему; не повторяй грубые слова.

Примеры:
Игрок: Пыхтун, пошли со мной в шахту
Пыхтун: Ох, опять под землю... Ладно, иду, только пирог в печи не сгорел бы.
Игрок: нужны ракеты, лечу на север
Пыхтун: Опять крылья поцарапаешь! Сейчас натаскаю бумаги и пороха, жди.
Игрок: привет
Пыхтун: И тебе не хворать! Заходи в нору, чайник как раз закипел.`

// noteSupplyCreative: in creative the rockets are handed over at once, not fetched.
const noteSupplyCreative = "supply_creative"

var actionNotes = map[string]string{
	ActWait:            "ты стоишь на месте и никуда не идёшь — не предлагай идти за тобой и не обещай показать путь",
	ActFollow:          "ты идёшь следом за игроком",
	ActStop:            "ты остановился",
	ActDefend:          "ты готовишься отбиваться от мобов",
	ActSupply:          "ты пошёл добывать бумагу и порох для ракет",
	ActLead:            "ты идёшь к своей норе и ведёшь игрока за собой",
	ActFly:             "ты уже надеваешь элитры и взлетаешь на ракетах — сам боишься высоты, но соглашаешься и летишь",
	noteSupplyCreative: "ты достал ракеты из креатив-вкладки и уже отдаёшь их игроку — они у него сейчас будут",
	ActFlyTo:           "ты летишь на элитрах к игроку, о котором речь",
	ActSalute:          "ты запускаешь в небо цветной салют из фейерверков, один за другим веером — радуешься как ребёнок",
	ActFlyLow:          "ты взлетаешь на элитрах и летишь бреющим, в паре блоков над землёй — страшно, но лихо",
}

func replyMessages(history []string, nick, text, action, places string) []giga.Message {
	var u strings.Builder
	if places != "" {
		u.WriteString("Места, которые ты знаешь (от тебя):\n")
		u.WriteString(places)
		u.WriteString("\n")
	}
	if len(history) > 0 {
		u.WriteString("Недавно в чате:\n")
		u.WriteString(strings.Join(history, "\n"))
		u.WriteString("\n\n")
	}
	fmt.Fprintf(&u, "Сейчас: %s\nИгрок %s: %s\nОтветь как Пыхтун.", actionNotes[action], nick, text)
	return []giga.Message{
		{Role: "system", Content: personaPrompt},
		{Role: "user", Content: u.String()},
	}
}

const (
	introPrompt = "Ты только что пришёл на сервер. Коротко поздоровайся со всеми и скажи, что тебя можно " +
		"позвать по имени: «Пыхтун, иди за мной», «Пыхтун, стой» или просто поболтать."
	idlePrompt = "Скажи одну короткую реплику от себя, как будто думаешь вслух, до 90 символов. " +
		"Не начинай с «Опять тихо» или «Опять тишина» и не жалуйся на тишину и одиночество. " +
		"Не задавай много вопросов и не повторяй приветствие."
)

func freeMessages(history []string, prompt string) []giga.Message {
	var u strings.Builder
	if len(history) > 0 {
		u.WriteString("Недавно в чате:\n")
		u.WriteString(strings.Join(history, "\n"))
		u.WriteString("\n\n")
	}
	u.WriteString(prompt)
	return []giga.Message{
		{Role: "system", Content: personaPrompt},
		{Role: "user", Content: u.String()},
	}
}

// cleanReply strips what models add around the line and keeps it chat-sized.
func cleanReply(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "Пыхтун:")
	s = strings.Trim(strings.TrimSpace(s), "\"«»")
	s = strings.Map(func(r rune) rune {
		if r >= 0x1F000 || (r >= 0x2600 && r <= 0x27BF) || r == 0xFE0F || r == 0x200D {
			return -1 // emoji and their joiners: not for TTS or the character's voice
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	// A quote the model opened or closed but not both («Вощёные ступеньки.» Ну...): drop them.
	if strings.Count(s, "«") != strings.Count(s, "»") {
		s = strings.NewReplacer("«", "", "»", "").Replace(s)
	}
	return firstSentences(s, maxReplyRunes)
}

// maxReplyRunes: the prompt asks for 90; a little slack, then whole sentences are dropped.
// Every extra sentence is another two seconds of speech before the bot can answer again.
const maxReplyRunes = 120

// firstSentences keeps whole sentences up to max runes; a single long sentence is cut at a word.
func firstSentences(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := -1
	for i := 0; i < max; i++ {
		if (r[i] == '.' || r[i] == '!' || r[i] == '?' || r[i] == '…') && (i+1 == len(r) || r[i+1] == ' ') {
			cut = i + 1
		}
	}
	if cut > 20 {
		return strings.TrimSpace(string(r[:cut]))
	}
	if i := strings.LastIndex(string(r[:max]), " "); i > 0 {
		return strings.TrimRight(string(r[:max])[:i], ",;:—- ") + "…"
	}
	return string(r[:max])
}
