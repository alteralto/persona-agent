package agent

import (
	"testing"
	"time"
)

// Lines from SkyBars, 21.11: what the bot said and what came back through players' microphones.
func TestOwnEcho(t *testing.T) {
	now := time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		said, heard string
		echo        bool
	}{
		{"Здесь я, фейерверки проверяю. Криперы близко — брови врозь!", "здесь я фейерверки проверяю крипера близко в брови в розе", true},
		{"Гуйскилл, моя лестница выше трёх ступенек уже катапультой считается.", "Пусть кем твоя лестница выше трав, ступенек уже катапульта считается", true},
		{"Смотрите все, фейерверк мой новый, красоты невиданной!", "Смотрите все, фейерверк мой новый, красота не виден", true},
		{"Нау? Нау что? Тут только фейерверки и криперы, ничем не помогу.", "Бля, что тут происходит?", false},
		{"Хм, Пушкин... А я думал, это очередная байка твоя.", "Ну, короче, это был Пушкин", false},
		{"Эй, Попаджкал, да опять за старое? Что случилось-то?", "за это бан, что ли?", false},
	} {
		a := &Agent{cfg: Config{BotNick: "p_agentttt", Aliases: []string{"пыхтун"}}}
		a.rememberSpoken(c.said, now, now.Add(4*time.Second))
		if got := a.ownEcho(c.heard, now.Add(3*time.Second)); got != c.echo {
			t.Errorf("ownEcho(%q after %q) = %v", c.heard, c.said, got)
		}
	}
	a := &Agent{cfg: Config{BotNick: "p_agentttt", Aliases: []string{"пыхтун"}}}
	a.rememberSpoken("Смотрите все, фейерверк мой новый, красоты невиданной!", now, now.Add(4*time.Second))
	if a.ownEcho("Смотрите все, фейерверк мой новый, красота не виден", now.Add(4*time.Second+echoAfterSpeech+time.Second)) {
		t.Error("an old line still counts")
	}
	// From the review: players answering him with his own words, or repeating one word, are players.
	for said, heard := range map[string]string{
		"На ферму иди прямо через мост":    "Пыхтун, а через мост на ферму идти безопасно?",
		"Нет, туда я не пойду":             "Пыхтун, нет, нет, нет",
		"Мост через реку у самой мельницы": "Мост, мост, мост!",
		"Мост через реку":                  "Пыхтун, через реку есть мост?",
	} {
		b := &Agent{cfg: Config{BotNick: "p_agentttt", Aliases: []string{"пыхтун"}}}
		b.rememberSpoken(said, now, now.Add(3*time.Second))
		if b.ownEcho(heard, now.Add(2*time.Second)) {
			t.Errorf("%q after %q taken for an echo", heard, said)
		}
	}
}
