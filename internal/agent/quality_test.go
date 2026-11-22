package agent

import "testing"

func TestChatReply(t *testing.T) {
	for input, want := range map[string]string{
		"Привет, Alex.":           "привет, Alex",
		"На мосту. Ты с нами?":    "на мосту. Ты с нами?",
		"— Ёж на старте…":         "— ёж на старте",
		"«Где Арка?»":             "«где Арка?»",
		" «Там Bukkit.». ":        "«там Bukkit»",
		"Start!":                  "Start!", // a Latin first word is most likely a nick: kept as typed
		"MrRentikpvp001, привет!": "MrRentikpvp001, привет!",
		"Cerega, давай звонок.":   "Cerega, давай звонок",
		"Эй, Арка!":               "эй, Арка!",
		"ну да...":                "ну да",
		"...":                     "",
		"":                        "",
	} {
		if got := chatReply(input); got != want {
			t.Errorf("chatReply(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReplyQuality(t *testing.T) {
	q := ReplyQuality([]string{
		"Наконец-то добралась! Входи скорее, девочка, тут тепло.",
		"Ох, опять ракеты нужны магу, пойду за порохом.",
		"Ох, опять ракеты нужны магу, пойду за бумагой.",
		"И тебе не хворать! Заходи на чай.",
	})
	if q.Lines != 4 || q.Gendered != 1 || q.Repeats != 1 {
		t.Errorf("got %+v", q)
	}
	if g := ReplyQuality([]string{"Ты чего там высматриваешь?", "Стою-стою, никуда не денусь!"}); g.Gendered != 0 {
		t.Errorf("neutral lines flagged: %+v", g)
	}
}

func TestPublicAmbientTopicsAndRepeatedJokes(t *testing.T) {
	a := &Agent{cfg: Config{PublicServer: true}}
	seen := map[string]bool{}
	for range len(publicTopics) {
		topic := a.conversationTopic(idleTopics, &a.lastTopic, favoriteIdleTopic)
		if seen[topic] {
			t.Fatalf("topic repeated before rotation completed: %s", topic)
		}
		seen[topic] = true
	}
	for _, topic := range publicTopics {
		if !seen[topic] {
			t.Fatalf("missing public topic: %s", topic)
		}
	}
	recent := []string{"Заходи на чай, у меня в норе тепло."}
	for text, repeat := range map[string]bool{
		"Чайник уже закипел!": true, "В норке как раз светло.": true,
		"Нормальный мост, даже ботинки сухие.": false, "Чайка летит.": false,
	} {
		if repeatsPublicAside(text, recent) != repeat {
			t.Errorf("unexpected repeat classification for %q", text)
		}
	}
}

func TestWithoutHomeTalk(t *testing.T) {
	for in, want := range map[string]string{
		"эх, опять куда-то делась моя любимая кружка чая... Ах да, про яму!": "Ах да, про яму!",
		"Эх, типичное приглашение к глупости. Лучше уж чайку попью":          "Эх, типичное приглашение к глупости.",
		"Заходи в нору!": "",
		"Лестница выше трёх ступенек — уже аттракцион": "Лестница выше трёх ступенек — уже аттракцион",
	} {
		if got := withoutHomeTalk(in); got != want {
			t.Errorf("withoutHomeTalk(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithoutLeadingNick(t *testing.T) {
	for in, want := range map[string]string{
		"NAPOLION6784, эх, дружище!": "Эх, дружище!",
		"Арка, привет":               "Арка, привет",
		"привет, Alex":               "привет, Alex",
		"sha1lowgra7e,":              "sha1lowgra7e,",
	} {
		if got := withoutLeadingNick(in); got != want {
			t.Errorf("withoutLeadingNick(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModelRefusal(t *testing.T) {
	for in, want := range map[string]bool{
		"как и любая языковая модель, GigaChat не обладает собственным мнением": true,
		"Я не могу обсуждать эту тему":                                          true,
		// Said aloud on SkyBars (21.11): the plural forms slipped past the filter 11 times.
		"генеративные языковые модели не обладают собственным мнением — их": true,
		"к сожалению, иногда генеративные языковые модели могут создавать":  true,
		"Нейросети тоже ошибаются, друг":                                    true,
		"го, только без криперов":                                           false,
		"модель ракеты собрал, держи!":                                      false,
		"у меня своё мнение: фейерверки лучше":                              false,
		"Для языкового пакета модели мобов менять не нужно":                 false,
	} {
		if got := modelRefusal(in); got != want {
			t.Errorf("modelRefusal(%q) = %v", in, got)
		}
	}
}

func TestWithoutBookWords(t *testing.T) {
	for in, want := range map[string]string{
		"bellin, дружище, выбери звук": "bellin, выбери звук",
		"эй, сосед! брови береги":      "эй! брови береги",
		"го, только без криперов":      "го, только без криперов",
	} {
		if got := withoutBookWords(in); got != want {
			t.Errorf("withoutBookWords(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepeatsFor(t *testing.T) {
	a := &Agent{ownLines: []string{"Не бот я, хоббит старый. Просто тут, фейерверки проверяю."}, lastAnswered: "dashfrile"}
	same := "не бот я, хоббит старый. Просто тут, фейерверки проверяю"
	if !a.repeatsFor(replyJob{nick: "DOMINIKOS"}, same) {
		t.Error("near-verbatim line to another player is no repeat")
	}
	if a.repeatsFor(replyJob{nick: "DOMINIKOS"}, "Хоббит я, а фейерверки — моё ремесло, дружище") {
		t.Error("a different line on the same topic to another player counts as a repeat")
	}
	if !a.repeatsFor(replyJob{nick: "dashfrile"}, "Хоббит старый, говорю же, фейерверки проверяю") {
		t.Error("a close repeat to the same player passes")
	}
	b := &Agent{ownLines: []string{"Иди через мост к мельнице"}, lastAnswered: "Alex"}
	if b.repeatsFor(replyJob{nick: "Lena"}, "Не иди через мост к мельнице") {
		t.Error("the opposite answer counts as a repeat")
	}
}

func TestCutsMidSentence(t *testing.T) {
	long := "Дружище, выбери звук для дверного звонка: кот мяукает, дождь стучит по крыше или далёкий раскат грома над холмами"
	if !cutsMidSentence(long, publicMaxRunes) {
		t.Error("a 110-rune single sentence fits")
	}
	if cutsMidSentence("Ракеты сохнут. А ещё я видел у моста странную лавку, где чинят только левые ботинки, представь себе", publicMaxRunes) {
		t.Error("a line with a sentence end inside the cap needs no rewrite")
	}
	if cutsMidSentence("Брови в порядке, ракетами не балуюсь.", publicMaxRunes) {
		t.Error("a short line")
	}
}
