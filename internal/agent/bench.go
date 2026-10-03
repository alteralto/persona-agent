package agent

import "github.com/alteralto/persona-agent/internal/giga"

// Scene is a fixed situation for comparing models on the character's voice.
type Scene struct {
	Name     string
	Messages []giga.Message
}

// BenchScenes returns the same prompts the agent builds in the game, for model comparison.
func BenchScenes() []Scene {
	history := []string{"NetTyan: Пончик, проверь основание уступа", "Pon4ikYT: Я снова сорвалась, здоровья мало"}
	return []Scene{
		{"привет", replyMessages(nil, "Chochok", "Пыхтун привет как дела?", ActWait, "")},
		{"иди за мной", replyMessages(history, "Pon4ikYT", "Пыхтун, иди за мной к дереву", ActFollow, "")},
		{"где нора", replyMessages(history, "NetTyan", "Пыхтун, где вход в твою нору?", ActWait,
			"- твоя нора (вход): x=-436 y=70 z=-135, 14 блоков на северо-запад\n")},
		{"грубость", replyMessages(nil, "Troll", "пыхтун ты тупой бот", ActWait, "")},
		{"сам заговорил", freeMessages(history, "Рядом с тобой Пончик. Обратись к Пончик по имени. "+
			"Тема: пожалуйся, что маг опять извёл все ракеты и придётся искать порох. Одна короткая фраза до 90 символов.")},
	}
}

// CleanReply exposes cleanReply for tools.
func CleanReply(s string) string { return cleanReply(s) }
