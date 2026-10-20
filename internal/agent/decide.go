package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/alteralto/persona-agent/internal/laya"
)

// Actions Laya chooses from. Keep the list short: accuracy drops as options grow.
const (
	ActWait      = "wait"
	ActFollow    = "follow"
	ActStop      = "stop"
	ActDefend    = "defend"
	ActSupply    = "supply"
	ActLead      = "lead"
	ActFly       = "fly"       // rule-only: not offered to Laya, keeps her option list short
	ActFlyTo     = "flyto"     // rule-only: fly to a player
	ActFlyLow    = "flylow"    // rule-only: a low-level flight (бреющий), a few blocks over the ground
	ActSalute    = "salute"    // rule-only: a fireworks show from the ground
	ActFlyTricks = "flytricks" // rule-only: a flight with aerobatic figures
	ActFlyRoute  = "flyroute"  // rule-only: a tour round places without landing
	ActRiddle    = "riddle"    // rule-only: a riddle about a place, a present to the first there
	ActRace      = "race"      // rule-only: a race to a place
	ActTreasure  = "treasure"  // rule-only: it hides a real item at a place and sets a riddle about it
)

const (
	SayReply  = "reply"
	SayIgnore = "silent"
)

var actionsRU = map[string]string{
	ActWait:   "ничего не делать, стоять рядом и слушать; просто разговор или вопрос",
	ActFollow: "игрок просит пойти за ним, идти следом, сопровождать его",
	ActStop:   "игрок просит остановиться, перестать, хватит, стоять на месте",
	ActDefend: "рядом опасность: мобы, зомби, криперы, нападение; нужно защищаться",
	ActSupply: "игрок просит принести ресурсы, бумагу, порох, фейерверки или ракеты",
	ActLead:   "игрок просит отвести, проводить, показать дорогу к норе или к входу, вести их",
}

var actionsEN = map[string]string{
	ActWait:   "do nothing, stay nearby and listen; just small talk or a question",
	ActFollow: "the player asks to come along, follow them, go with them",
	ActStop:   "the player asks to stop, cancel, enough, stay where you are",
	ActDefend: "danger nearby: mobs, zombies, creepers, an attack; defend yourself",
	ActSupply: "the player asks to bring resources, paper, gunpowder, fireworks or rockets",
	ActLead:   "the player asks to be led or guided to your burrow or its entrance, to show the way",
}

var sayRU = map[string]string{
	SayReply:  "к персонажу обращаются, задают вопрос, просят или здороваются — нужно ответить",
	SayIgnore: "сообщение не персонажу, служебное или игроки говорят между собой — промолчать",
}

// Criteria are the option descriptions Laya scores. They live in data/laya_criteria.json when a
// tuning run (cmd/layatune) found better wording than the defaults below.
type Criteria struct {
	RU      map[string]string `json:"ru"`
	EN      map[string]string `json:"en"`
	Mode    string            `json:"mode,omitempty"`    // decision rule, see decideWith
	MinProb float64           `json:"minProb,omitempty"` // 0: the agent's configured threshold
}

var criteria = Criteria{RU: actionsRU, EN: actionsEN}

// DefaultCriteria returns the built-in option descriptions.
func DefaultCriteria() Criteria { return Criteria{RU: actionsRU, EN: actionsEN} }

// CurrentCriteria returns what Laya is asked with now.
func CurrentCriteria() Criteria { return criteria }

// UseCriteria replaces the option descriptions; the keys must be the same actions.
func UseCriteria(c Criteria) error {
	for k := range actionsRU {
		if c.RU[k] == "" || c.EN[k] == "" {
			return fmt.Errorf("criteria: missing %q", k)
		}
	}
	criteria = c
	return nil
}

// LoadCriteria reads tuned criteria from path; a missing file keeps the defaults.
func LoadCriteria(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var c Criteria
	if err := json.Unmarshal(raw, &c); err != nil {
		return false, err
	}
	return true, UseCriteria(c)
}

// Questions builds the Laya questions with the current criteria.
func Questions(botNick string) map[string]laya.Question {
	return QuestionsWith(botNick, criteria)
}

// QuestionsWith builds the Laya questions with the given criteria (used by tuning).
func QuestionsWith(botNick string, c Criteria) map[string]laya.Question {
	return map[string]laya.Question{
		"act_ru": {Type: "choice", Instructions: "Что должен сделать бот " + botNick + " в игре?", Criteria: c.RU},
		"act_en": {Type: "choice", Instructions: "What should the bot " + botNick + " do in the game?", Criteria: c.EN},
		"say":    {Type: "choice", Instructions: "Должен ли " + botNick + " ответить на последнее сообщение?", Criteria: sayRU},
	}
}

type Decision struct {
	Action     string
	ActionConf float64
	Agreed     bool
	Reply      bool
	ReplyConf  float64
}

// decide applies the two-language agreement rule: the action is accepted only when the
// Russian and English formulations pick the same option with enough average probability.
// Laya's own reply/silent split sits near 50/50 on game chat, so replying is decided by
// the caller (name mention, accepted command) and Laya's vote counts only when it is clear.
func decide(a map[string]laya.Answer, minProb float64) Decision {
	mode := criteria.Mode
	if criteria.MinProb > 0 {
		minProb = criteria.MinProb
	}
	return decideWith(a, mode, minProb)
}

// decideWith applies one decision rule:
//   - "agree" (default): both languages pick the same option, average probability >= minProb;
//   - "ru" / "en": trust one language alone;
//   - "max": take the more confident language.
func decideWith(a map[string]laya.Answer, mode string, minProb float64) Decision {
	ru, en, say := a["act_ru"], a["act_en"], a["say"]
	pr, pe := ru.Probabilities[ru.Choice], en.Probabilities[en.Choice]
	d := Decision{Action: ActWait, ReplyConf: say.Probabilities[SayReply]}
	choice, prob := "", 0.0
	switch mode {
	case "ru":
		choice, prob = ru.Choice, pr
	case "en":
		choice, prob = en.Choice, pe
	case "max":
		choice, prob = ru.Choice, pr
		if pe > pr {
			choice, prob = en.Choice, pe
		}
	default:
		prob = (pr + pe) / 2
		if ru.Choice == en.Choice {
			choice = ru.Choice
		}
	}
	d.ActionConf = prob
	if choice != "" && prob >= minProb {
		d.Action, d.Agreed = choice, true
	}
	d.Reply = say.Choice == SayReply && d.ReplyConf >= 0.65
	return d
}

// leadRule catches explicit "lead us home" requests Laya keeps missing (ru/en disagree).
var leadRule = regexp.MustCompile(`(?i)(веди|проводи|отведи|проведи|покажи\s+(где\s+)?(вход|дорог|нор|путь))`)

// Keyword rules for explicit requests Laya keeps missing. They only fire on unambiguous
// verbs; everything else is Laya's call. Go's  is ASCII-only, so words are matched as
// substrings with care.
var (
	flyToRule     = regexp.MustCompile(`(?i)(прилет(ай|и)|лети\s+(ко мне|к нам|сюда|к\s+[^\s,.!?]+))`)
	flyRule       = regexp.MustCompile(`(?i)(полетели|полетаем|взлетай|взлетаем|летим|на элитрах)`)
	flyTricksRule = regexp.MustCompile(`(?i)(трюк|пилотаж|бочк[уа]|фигур[уы]\s+в\s+небе|покажи\s+фигур|петл[юя])`)
	flyRouteRule  = regexp.MustCompile(`(?i)(облет|облёт|облети|патрул|покажи\s+окрестност\S*\s+с\s+(воздуха|неба))`)
	flyLowRule    = regexp.MustCompile(`(?i)(бреющ|(пролети|лети|летай|полетай|полетели)\s+(пониже|низко|над\s+самой\s+землёй))`)
	saluteRule    = regexp.MustCompile(`(?i)(салют|(запусти|устрой|покажи|пусти)\s+фейерверк|фейерверк(и|ов)?\s+(запусти|покажи|давай))`)
	riddleRule    = regexp.MustCompile(`(?i)(загада(й|ешь)|загадк|квест|поиграем|сыграем|давай\s+(игр|поигра|сыгра))`)
	// Whole words: "склад", "клади", "кладбище", "вклад" are not the game (\b is ASCII-only in Go).
	treasureRule = regexp.MustCompile(`(?i)(^|[^а-яё])(клад(а|у|ом|е|ы|ов|ик)?|сокровищ[а-яё]*)($|[^а-яё])`)
	raceRule     = regexp.MustCompile(`(?i)(гонк[уаи]|наперегонки|кто\s+(первый|первым|быстрее)\s+(долетит|добежит|доберётся|доберется))`)
	supplyRule   = regexp.MustCompile(`(?i)(принеси|достань|подкинь|дай|нужн[ыо]).{0,40}(ракет|бумаг|порох|фейерверк)`)
	defendRule   = regexp.MustCompile(`(?i)(крипер|зомби|скелет|паук|напал[иа]|мобы? рядом)`)
	stopRule     = regexp.MustCompile(`(?i)(^|пыхтун[,! ]+)(стой|замри|подожди|постой)|подожд(и|ёте|ете) (меня|со мной)`)
	followRule   = regexp.MustCompile(`(?i)(подойди|вернись|иди ко мне|иди сюда|пошли со мной|пойд[её]м со мной|за мной)`)
	// talkAboutMobs: "ты боишься криперов?" names a mob but is small talk, not an alarm.
	talkAboutMobs = regexp.MustCompile(`(?i)(бои(шь|т)ся|бояться|любишь|нравя?тся|видел|расскажи|про\s|какие|сколько)`)
	urgentMobs    = regexp.MustCompile(`(?i)(рядом|сзади|тут|здесь|ид[уё]т|напал|помоги|защищ|спаса|осторожн|берегись)`)
)

// applyRules overrides Laya when the request is unambiguous, in order of specificity.
func applyRules(d Decision, text string) (Decision, bool) {
	for _, r := range []struct {
		re     *regexp.Regexp
		action string
	}{
		{supplyRule, ActSupply},
		{raceRule, ActRace},         // "гонка на элитрах" is a race, not a flight
		{treasureRule, ActTreasure}, // "давай поиграем в клад" is the treasure, not any game
		{riddleRule, ActRiddle},
		{saluteRule, ActSalute},
		{flyToRule, ActFlyTo},
		{flyTricksRule, ActFlyTricks},
		{flyRouteRule, ActFlyRoute},
		{flyLowRule, ActFlyLow},
		{flyRule, ActFly},
		{defendRule, ActDefend},
		{stopRule, ActStop},
		{followRule, ActFollow},
		{leadRule, ActLead},
	} {
		if r.re.MatchString(text) {
			if r.action == ActDefend && talkAboutMobs.MatchString(text) && !urgentMobs.MatchString(text) {
				continue
			}
			d.Action, d.Agreed = r.action, true
			return d, true
		}
	}
	return d, false
}

// situation renders recent context for Laya: older lines first, the trigger last.
func situation(botNick string, history []string, nick, text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Бот %s в Minecraft.\n", botNick)
	if len(history) > 0 {
		b.WriteString("Недавно в чате:\n")
		for _, h := range history {
			b.WriteString(h)
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "Сейчас игрок %s пишет: %s", nick, text)
	return b.String()
}

// Probe runs one Laya decision for a line of chat; used by the bench command.
func Probe(ctx context.Context, l *laya.Client, botNick, nick, text string, minConf float64) (Decision, error) {
	a, err := l.Decide(ctx, situation(botNick, nil, nick, text), Questions(botNick))
	if err != nil {
		return Decision{}, err
	}
	return decide(a, minConf), nil
}

// Situation renders the Laya state for a single line (used by tuning).
func Situation(botNick, nick, text string) string { return situation(botNick, nil, nick, text) }

// Decide applies the agent's decision rule to Laya's answers (used by tuning).
func Decide(a map[string]laya.Answer, minProb float64) Decision { return decide(a, minProb) }

// DecideWith applies an explicit rule (used by tuning).
func DecideWith(a map[string]laya.Answer, mode string, minProb float64) Decision {
	return decideWith(a, mode, minProb)
}

// ApplyRules exposes the keyword rules that sit on top of Laya (used by tuning).
func ApplyRules(d Decision, text string) Decision {
	d, _ = applyRules(d, text)
	return d
}

// flyTargetWord returns the name after "лети к" ("лети к нетян" → "нетян"), or "" for
// "ко мне", "к нам", "сюда" and "прилетай", which mean the speaker.
func flyTargetWord(text string) string {
	m := regexp.MustCompile(`(?i)лети\s+к\s+([^\s,.!?]+)`).FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	if w := strings.ToLower(m[1]); w == "мне" || w == "нам" {
		return ""
	}
	return m[1]
}
