package agent

import (
	"strings"
	"testing"

	"github.com/alteralto/persona-agent/internal/laya"
)

func TestDecideAgreement(t *testing.T) {
	ans := func(ru, en string, cru, cen float64, say string) map[string]laya.Answer {
		return map[string]laya.Answer{
			"act_ru": {Choice: ru, Probabilities: map[string]float64{ru: cru}},
			"act_en": {Choice: en, Probabilities: map[string]float64{en: cen}},
			"say":    {Choice: say, Probabilities: map[string]float64{SayReply: 0.9}},
		}
	}
	cases := []struct {
		name   string
		in     map[string]laya.Answer
		action string
		reply  bool
	}{
		{"agree and confident", ans(ActFollow, ActFollow, 0.9, 0.8, SayReply), ActFollow, true},
		{"disagree -> wait", ans(ActFollow, ActStop, 0.9, 0.9, SayReply), ActWait, true},
		{"low probability -> wait", ans(ActStop, ActStop, 0.4, 0.3, SayIgnore), ActWait, false},
		{"missing answers -> wait", map[string]laya.Answer{}, ActWait, false},
	}
	for _, c := range cases {
		d := decide(c.in, 0.5)
		if d.Action != c.action || d.Reply != c.reply {
			t.Errorf("%s: got action=%s reply=%v, want %s %v", c.name, d.Action, d.Reply, c.action, c.reply)
		}
	}
}

func TestCleanReply(t *testing.T) {
	cases := map[string]string{
		"Пыхтун: Ох, иду!":        "Ох, иду!",
		"  «Ладно, ладно»  ":      "Ладно, ладно",
		"строка\nвторая   строка": "строка вторая строка",
		"Попьём чайку 😊":          "Попьём чайку",
		"Ох, ну что интересного рассказать... Помню я однажды нашёл редстоун глубоко под землёй. Целый день потом искал дорогу назад!": "Ох, ну что интересного рассказать... Помню я однажды нашёл редстоун глубоко под землёй.",
	}
	for in, want := range cases {
		if got := cleanReply(in); got != want {
			t.Errorf("cleanReply(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLeadRule(t *testing.T) {
	undecided := Decision{Action: ActWait}
	for _, text := range []string{"Пыхтун веди нас к входу", "проводи до норы", "покажи где вход", "Покажи дорогу"} {
		if d, ok := applyRules(undecided, text); !ok || d.Action != ActLead {
			t.Errorf("%q: got %+v ok=%v", text, d, ok)
		}
	}
	for _, text := range []string{"привет", "покажи инвентарь"} {
		if _, ok := applyRules(undecided, text); ok {
			t.Errorf("%q should not match", text)
		}
	}
	// An explicit verb outranks Laya: "веди" means lead even if Laya said follow.
	if d, _ := applyRules(Decision{Action: ActFollow, Agreed: true}, "веди нас"); d.Action != ActLead {
		t.Errorf("explicit lead verb must win, got %s", d.Action)
	}
}

func TestAddressedTo(t *testing.T) {
	names := []string{"пончик", "пон", "nettyan", "нетян"}
	cases := map[string]bool{
		"Пон, с какой стороны у дерева проверен обход?": true,
		"NetTyan я поднялась на уступ":                  true,
		"Пончик моя позиция минус 432":                  true,
		"Ветки по одной":                                false,
		"Поняла.":                                       false,
		"Я у травяного уступа, а Пончик внизу":          false, // named late: not an address
	}
	for text, want := range cases {
		if got := addressedTo(text, names); got != want {
			t.Errorf("addressedTo(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestAsksWhere(t *testing.T) {
	for text, want := range map[string]bool{
		"Пыхтун, где вход в твою нору?": true,
		"куда идти к розовой крыше":     true,
		"привет как дела":               false,
	} {
		if got := asksWhere(text); got != want {
			t.Errorf("asksWhere(%q) = %v", text, got)
		}
	}
}

func TestFlyRule(t *testing.T) {
	for _, text := range []string{"Пыхтун, полетели!", "давай на элитрах", "Пыхтун взлетай"} {
		if d, ok := applyRules(Decision{Action: ActWait}, text); !ok || d.Action != ActFly {
			t.Errorf("%q: %+v %v", text, d, ok)
		}
	}
	if _, ok := applyRules(Decision{Action: ActWait}, "привет, как дела"); ok {
		t.Error("plain chat must not trigger flight")
	}
}

func TestKeywordRules(t *testing.T) {
	cases := map[string]string{
		"нужны ракеты для полёта, принеси штук десять": ActSupply, // not flight
		"подкинь фейерверков, у мага кончились":        ActSupply,
		"крипер сзади!":             ActDefend,
		"Пыхтун, замри":             ActStop,
		"подожди меня тут":          ActStop,
		"Пыхтун, подойди сюда":      ActFollow,
		"Вернись к нам, пожалуйста": ActFollow,
		"Пыхтун, полетели!":         ActFly,
		"пролети бреющим":           ActFlyLow,
		"Пыхтун, полетели низко":    ActFlyLow,
		"лети пониже, над крышами":  ActFlyLow,
		"Пыхтун, устрой салют!":     ActSalute,
		"покажи трюки":              ActFlyTricks,
		"Пыхтун, сделай бочку!":     ActFlyTricks,
		"облети окрестности":        ActFlyRoute,
		"полетели в патруль":        ActFlyRoute,
		"запусти фейерверки":        ActSalute,
	}
	for text, want := range cases {
		if d, ok := applyRules(Decision{Action: ActWait}, text); !ok || d.Action != want {
			t.Errorf("%q: got %s (%v), want %s", text, d.Action, ok, want)
		}
	}
	for _, text := range []string{"Я стою на сухом дёрне", "Поняла, остаюсь здесь", "а ты давно тут живёшь?"} {
		if d, ok := applyRules(Decision{Action: ActWait}, text); ok {
			t.Errorf("%q should be left to Laya, got %s", text, d.Action)
		}
	}
}

func TestOverlap(t *testing.T) {
	if o := overlap("Пыхтун, подойди к нам с чаем", "подойди пожалуйста к нам"); o < 0.4 {
		t.Errorf("repeat not detected: %v", o)
	}
	if o := overlap("привет как дела", "иди за мной"); o != 0 {
		t.Errorf("unrelated lines overlap: %v", o)
	}
}

func TestPromisesMove(t *testing.T) {
	for text, want := range map[string]bool{
		"Ох, ну ладно, иду, только не спеши":   true,
		"Следуй за мной, покажу норку":         true,
		"Вход там, у берёзы, на северо-западе": false,
		"Жду тут, держись крепче":              false,
	} {
		if got := promisesMove(text); got != want {
			t.Errorf("promisesMove(%q) = %v", text, got)
		}
	}
}

func TestMentions(t *testing.T) {
	names := []string{"arcmlchikkk", "пыхтун", "арк"}
	for text, want := range map[string]bool{
		"Арк, я подошла к тебе":       true,
		"арк.":                        true,
		"Пыхтуна видели у норы":       true,
		"arcmlchikkk привет":          true,
		"мы в парке у арки":           false,
		"новая точка, подо мной дерн": false,
	} {
		if got := mentions(text, names); got != want {
			t.Errorf("mentions(%q) = %v", text, got)
		}
	}
}

func TestFlyToRule(t *testing.T) {
	for text, target := range map[string]string{
		"Пыхтун, прилетай ко мне": "",
		"лети к нам скорее":       "",
		"Пыхтун, лети к Нетян":    "Нетян",
		"лети к NetTyan!":         "NetTyan",
	} {
		d, ok := applyRules(Decision{Action: ActWait}, text)
		if !ok || d.Action != ActFlyTo {
			t.Errorf("%q: got %s", text, d.Action)
		}
		if got := flyTargetWord(text); got != target {
			t.Errorf("%q: target %q, want %q", text, got, target)
		}
	}
	if d, _ := applyRules(Decision{Action: ActWait}, "Пыхтун, полетели!"); d.Action != ActFly {
		t.Errorf("plain flight must stay fly, got %s", d.Action)
	}
}

func TestDefendNotOnSmallTalk(t *testing.T) {
	for _, text := range []string{"Пыхтун, а ты боишься криперов?", "расскажи про зомби", "какие мобы тебе нравятся"} {
		if d, _ := applyRules(Decision{Action: ActWait}, text); d.Action == ActDefend {
			t.Errorf("%q: small talk about mobs turned on defence", text)
		}
	}
	for _, text := range []string{"крипер рядом!", "зомби напали", "Пыхтун, помоги, скелеты", "осторожно, паук"} {
		if d, ok := applyRules(Decision{Action: ActWait}, text); !ok || d.Action != ActDefend {
			t.Errorf("%q: want defend, got %s", text, d.Action)
		}
	}
}

func TestFirstSentencesLongSentence(t *testing.T) {
	s := strings.Repeat("слово ", 40) // 240 runes, no sentence end
	got := firstSentences(strings.TrimSpace(s), 120)
	if n := len([]rune(got)); n > 121 || !strings.HasSuffix(got, "…") || strings.Contains(got, "  ") {
		t.Errorf("got %d runes: %q", n, got)
	}
}
