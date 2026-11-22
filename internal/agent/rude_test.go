package agent

import (
	"testing"
	"time"
)

func TestRude(t *testing.T) {
	for in, want := range map[string]bool{
		"Ты жопа": true,
		"ти жопа": true,
		"Королевский, ты чё охуел?":        false, // swearing, not an insult
		"заткнись уже":                     true,
		"сука":                             false, // swearing: "Сука, Пыхтун, где ферма?"
		"ты урод":                          true,
		"Тут уродилась морковь":            false,
		"Какой уродливый дом":              false,
		"чмоки, до встречи":                false,
		"Туран, лучше страхуйся верёвкой!": false,
		"дурацкое название для лавки":      false,
		"сукно и лохматый пёс":             false,
		"оскорбление":                      false,
		"Как тебя зовут?":                  false,
		"Агент, ты тут? Бля, ебануться":    false, // swearing, not an insult: mutes nobody
		"Что блять флаг украины?":          false,
	} {
		if got := rude(in); got != want {
			t.Errorf("rude(%q) = %v", in, got)
		}
	}
}

func TestUnfitForHim(t *testing.T) {
	for in, want := range map[string]bool{
		"Эй, ___xooox__, ну ты даёшь! Жопа так жопа, нечего тут обижаться.": true,
		"Фу, такое в моей норе не говорят.":                                 false,
		"Ох, блин, опять ракеты кончились":                                  false,
		"да пошло оно всё нахуй":                                            true,
		"Ёб твою мать":                     true,
		"Сука, опять ракеты кончились":     true,
		"Туран, лучше страхуйся верёвкой!": false,
	} {
		if got := unfitForHim(in); got != want {
			t.Errorf("unfitForHim(%q) = %v", in, got)
		}
	}
}

// The troll of 21.11, 20:21-20:33: one answer, then quiet, then muted even when naming the bot.
func TestRudeGate(t *testing.T) {
	a := &Agent{}
	now := time.Date(2026, 11, 21, 20, 21, 0, 0, time.UTC)
	step := func(text string) bool {
		now = now.Add(7 * time.Second)
		skip, _ := a.rudeGate("___xooox___", text, now, true)
		return skip
	}
	if step("Ты жопа") {
		t.Fatal("first rude line gets one answer")
	}
	if step("Как у тебя дела?") {
		t.Fatal("a polite line is judged as usual")
	}
	if !step("ти жопа") {
		t.Fatal("second rude line within the window is answered")
	}
	if !step("те жопа") || !step("Тихо, пыхтун") {
		t.Fatal("third rude line does not mute the player")
	}
	if skip, _ := a.rudeGate("Lena", "Привет", now, true); skip {
		t.Fatal("someone else is muted")
	}
	for i := 0; i < 5; i++ { // a fight between players far off
		if skip, _ := a.rudeGate("Rob", "ты дебил", now, false); skip {
			t.Fatal("an insult not said to the bot counts")
		}
	}
	now = now.Add(rudeMuteFor)
	if step("Как тебя зовут?") {
		t.Fatal("still muted after the mute ran out")
	}
}
