package agent

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

// Games with the players around, played on the game's own terms and judged by where people are:
// a riddle about a place it knows (the first to get there gets a present) and a race to a place.
// It builds nothing: the places are the ones it has found, the presents come from the creative
// tab and are handed over with @give.

const (
	gameRiddle = "riddle"
	gameRace   = "race"

	gameWinR    = 6.0              // blocks from the place that count as there
	gameEvery   = 10 * time.Minute // its own idea for a game at most this often
	riddleFor   = 5 * time.Minute
	riddleHint  = 100 * time.Second // no winner yet: a plainer second clue
	raceFor     = 3 * time.Minute
	raceStartIn = 6 * time.Second // the announcement, then "Старт!"
)

// Riddle places in sight of the players round the bot (the client tracks players ~120 blocks
// out, so a winner further off would go unseen); race places a proper run away.
var (
	riddleRange = [2]float64{35, 110}
	raceRange   = [2]float64{60, 140}
)

type game struct {
	kind    string
	place   explore.Place
	started time.Time
	hinted  bool
	running bool // the race has started
	with    []string
}

// presents: what the winner may get, one picked at random (item id, count, how it is said).
var presents = []struct {
	item  string
	count int
	say   string
}{
	{"firework_rocket", 16, "16 ракет для элитр"},
	{"golden_apple", 2, "два золотых яблока"},
	{"cake", 1, "торт"},
	{"cookie", 16, "16 печенек"},
	{"pumpkin_pie", 4, "четыре тыквенных пирога"},
}

// gamePlace picks a place for a game, dist from the bot within r, not one played lately.
// Buildings, water and landmarks; not signs (a sign's words make a poor riddle).
func (a *Agent) gamePlace(r [2]float64) (explore.Place, bool) {
	var pick []explore.Place
	for _, p := range a.places.List {
		d := math.Hypot(float64(p.X)-a.pos.X, float64(p.Z)-a.pos.Z)
		if d < r[0] || d > r[1] || strings.HasPrefix(p.Desc, "табличка") || slices.Contains(a.gameRecent, p.Desc) {
			continue
		}
		pick = append(pick, p)
	}
	if len(pick) == 0 {
		return explore.Place{}, false
	}
	return pick[rand.IntN(len(pick))], true
}

// startGame starts a riddle or a race for the players near; false (and a word why, when asked)
// when there is no place for it.
func (a *Agent) startGame(ctx context.Context, kind, asker string) bool {
	if a.game != nil || !a.havePos || a.places == nil {
		return false
	}
	var with []string
	for _, p := range a.visiblePlayers(ctx) {
		if p.dist <= visitNear {
			with = append(with, p.name)
		}
	}
	r := riddleRange
	if kind == gameRace {
		r = raceRange
	}
	pl, ok := a.gamePlace(r)
	if !ok {
		a.log.Info("game: no place for it", "kind", kind)
		if asker != "" {
			a.enqueue(replyJob{prompt: fmt.Sprintf("Игрок %s позвал тебя поиграть, а ты не знаешь рядом ни одного "+
				"подходящего места для игры. Скажи об этом одной фразой до 120 символов и пообещай сначала осмотреться.",
				spokenName(asker)), history: slices.Clone(a.history), at: time.Now()})
		}
		return false
	}
	a.game = &game{kind: kind, place: pl, started: time.Now(), with: with}
	a.lastGame = time.Now()
	a.gameRecent = append(a.gameRecent, pl.Desc)
	if len(a.gameRecent) > 6 {
		a.gameRecent = a.gameRecent[1:]
	}
	a.lingerFor(riddleFor) // stays about: it has to see who gets there
	d, dir := a.placeDistDir(pl)
	who := strings.Join(spokenNames(with), ", ")
	a.log.Info("game: start", "kind", kind, "place", pl.Desc, "x", pl.X, "z", pl.Z, "dist", d, "with", who)
	var prompt string
	if kind == gameRiddle {
		prompt = fmt.Sprintf("Ты затеял игру для игроков рядом (%s): загадку-место. Загадай им место так, чтобы его "+
			"можно было найти, но не сразу. Место: %s. Оно примерно в %d блоках от тебя, на %s. Не называй координаты "+
			"и не повторяй описание дословно. Скажи, что первый, кто доберётся туда, получит от тебя подарок. "+
			"Одна-две фразы до 200 символов, в характере.", who, pl.Desc, d, dir)
	} else {
		prompt = fmt.Sprintf("Ты объявляешь гонку для игроков рядом (%s): кто первым доберётся до места — %s, "+
			"на %s, примерно %d блоков. Ты тоже участвуешь и полетишь на элитрах, хоть и боишься. Скажи, что старт "+
			"через пять секунд. Одна-две фразы до 200 символов, в характере.", who, pl.Desc, dir, d)
	}
	a.enqueue(replyJob{prompt: prompt, history: slices.Clone(a.history), at: time.Now()})
	return true
}

// checkGame judges a game under way: a winner, a clue, the race start, the end of time.
func (a *Agent) checkGame(ctx context.Context, pos explore.Pos) {
	g := a.game
	if g == nil {
		return
	}
	px, pz := float64(g.place.X), float64(g.place.Z)
	if g.kind == gameRace && !g.running && time.Since(g.started) >= raceStartIn {
		g.running = true
		a.sayNow(ctx, "На старт… внимание… марш!")
		if !a.startFlight(ctx, g.place.X, g.place.Z) {
			if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", g.place.X, g.place.Z)}); err != nil {
				a.log.Warn("race: walk", "err", err)
			}
		}
		return
	}
	if g.kind == gameRace && !g.running {
		return // before the start nobody wins
	}
	for _, p := range a.visiblePlayers(ctx) {
		if math.Hypot(p.x-px, p.z-pz) <= gameWinR {
			a.endGame(ctx, p.name)
			return
		}
	}
	if g.kind == gameRace && math.Hypot(pos.X-px, pos.Z-pz) <= gameWinR {
		a.endGame(ctx, a.cfg.BotNick)
		return
	}
	limit := riddleFor
	if g.kind == gameRace {
		limit = raceFor
	}
	if time.Since(g.started) > limit {
		d, dir := a.placeDistDir(g.place)
		a.log.Info("game: nobody won", "kind", g.kind, "place", g.place.Desc)
		a.game = nil
		a.enqueue(replyJob{prompt: fmt.Sprintf("Никто так и не добрался до загаданного места (%s, на %s, около %d "+
			"блоков). Раскрой ответ и подшути по-доброму, одной-двумя фразами до 160 символов.", g.place.Desc, dir, d),
			history: slices.Clone(a.history), at: time.Now()})
		return
	}
	if g.kind == gameRiddle && !g.hinted && time.Since(g.started) > riddleHint {
		g.hinted = true
		d, dir := a.placeDistDir(g.place)
		a.enqueue(replyJob{prompt: fmt.Sprintf("Твою загадку-место пока никто не разгадал. Место: %s, на %s от тебя, "+
			"около %d блоков. Дай вторую подсказку, понятнее первой, одной фразой до 140 символов.", g.place.Desc, dir, d),
			history: slices.Clone(a.history), at: time.Now()})
	}
}

// endGame congratulates the winner and hands over a present (in creative, from the creative tab).
func (a *Agent) endGame(ctx context.Context, winner string) {
	g := a.game
	a.game = nil
	a.log.Info("game: won", "kind", g.kind, "winner", winner, "place", g.place.Desc,
		"seconds", int(time.Since(g.started).Seconds()))
	if strings.EqualFold(winner, a.cfg.BotNick) {
		a.mood("happy", 8)
		a.enqueue(replyJob{prompt: "Ты сам первым долетел до финиша гонки. Похвастайся по-доброму и позови на " +
			"реванш, одной фразой до 140 символов.", history: slices.Clone(a.history), at: time.Now()})
		return
	}
	gift := presents[rand.IntN(len(presents))]
	gave := a.creative && a.fork && a.creativeGive(ctx, gift.item, gift.count)
	if gave {
		cmd := fmt.Sprintf("@give %s %s %d", winner, gift.item, gift.count)
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": cmd}); err != nil {
			a.log.Warn("game: give", "err", err)
			gave = false
		}
	}
	what := "свою благодарность (подарка под рукой нет)"
	if gave {
		what = gift.say
	}
	kind := "первым нашёл загаданное место"
	if g.kind == gameRace {
		kind = "первым пришёл к финишу гонки"
	}
	a.mood("happy", 8)
	if a.people != nil {
		if err := a.people.Append(winner, "(в игре "+kind+": "+g.place.Desc+")", "подарил: "+what); err != nil {
			a.log.Warn("people memory", "err", err)
		}
	}
	a.enqueue(replyJob{prompt: fmt.Sprintf("Игрок %s %s (%s)! Поздравь его и скажи, что несёшь ему подарок: %s. "+
		"Одной-двумя фразами до 160 символов, в характере.", spokenName(winner), kind, g.place.Desc, what),
		history: slices.Clone(a.history), at: time.Now()})
}

// placeDistDir: how far a place is from the bot, rounded to tens, and which way.
func (a *Agent) placeDistDir(p explore.Place) (int, string) {
	d := math.Hypot(float64(p.X)-a.pos.X, float64(p.Z)-a.pos.Z)
	return int(math.Round(d/10) * 10), explore.Direction(a.pos, float64(p.X), float64(p.Z))
}

func spokenNames(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = spokenName(n)
	}
	return out
}

// sayNow says a fixed line at once, to the chat and aloud (a countdown cannot wait for the model).
func (a *Agent) sayNow(ctx context.Context, text string) {
	a.lastSpoke.Store(time.Now().UnixNano())
	if _, err := a.mc.Call(ctx, "ChatMessage", map[string]any{"msg": text}); err != nil {
		a.log.Warn("say", "err", err)
		return
	}
	if a.speaker != nil {
		if err := a.speaker.Say(ctx, text); err != nil {
			a.log.Warn("voice", "err", err)
		}
	}
	a.log.Info("said", "model", "fixed", "text", text)
}
