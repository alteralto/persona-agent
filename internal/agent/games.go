package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

// Games with the players around, played on the game's own terms and judged by where people are:
// a riddle about a place it knows (the first to get there gets a present), with "hot and cold"
// calls as the players search, and a race to a place it judges from the finish. It builds
// nothing: the places are the ones it has found, the presents come from the creative tab and are
// handed over with @give, the signals are its fireworks.

const (
	gameRiddle   = "riddle"
	gameRace     = "race"
	gameTreasure = "treasure" // a riddle about where it has left a real present on the ground

	gameWinR  = 6.0              // blocks from the place that count as there
	gameEvery = 10 * time.Minute // its own idea for a game at most this often
	judgeNear = 8.0              // the judge is at the finish, the beacon over the place

	heatEvery  = 10 * time.Second // one "warmer/colder" call at most this often
	raceAhead  = 20.0             // at the start nearer the finish than this: not in the race
	beaconWait = 2 * time.Minute  // the flight to the place to light it up, at most
)

// Timings tests shorten (export_test.go).
var (
	riddleFor  = 5 * time.Minute
	riddleHint = 100 * time.Second // no winner yet: a plainer second clue
	heatQuiet  = 15 * time.Second  // the riddle is said before any "warmer/colder"
	raceFor    = 3 * time.Minute   // from the start signal
	raceSetup  = 90 * time.Second  // the flight to the finish; the start is given after it at the latest
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
	started time.Time // the announcement; for a race, the start signal once given
	hinted  bool
	running bool // the race has started
	with    []string

	heat     map[string]int  // riddle: how warm each searcher was at the last look
	lastHeat time.Time       // riddle: the last "warmer/colder"
	ahead    map[string]bool // at the place already when the race or the search started: not in it

	phase   string    // treasure: "hide" (off to the place), "back" (to the players), "" (the search)
	origin  [2]int    // treasure: where the players wait
	backAt  time.Time // treasure: when it set off back
	gift    int       // treasure: which present it hides
	dropped bool      // treasure: the present lies there (else it is handed over by @give)
}

// beacon: after a riddle nobody solved, it flies to the place and fires a rocket over it.
type beacon struct {
	x, z  int
	desc  string
	until time.Time
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
	if kind == gameTreasure && !a.fork {
		kind = gameRiddle // nothing to drop with: the plain riddle
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
		a.log.Info("game: no place for it", "kind", kind, "x", explore.Block(a.pos.X), "z", explore.Block(a.pos.Z),
			"places", len(a.places.List))
		if asker != "" {
			a.enqueue(replyJob{prompt: fmt.Sprintf("Игрок %s позвал тебя поиграть, а ты не знаешь рядом ни одного "+
				"подходящего места для игры. Скажи об этом одной фразой до 120 символов и пообещай сначала осмотреться.",
				spokenName(asker)), history: slices.Clone(a.history), at: time.Now()})
		}
		return false
	}
	a.game = &game{kind: kind, place: pl, started: time.Now(), with: with}
	a.beacon = nil
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
	switch kind {
	case gameRiddle:
		prompt = riddlePrompt(a.game, who, d, dir)
	case gameTreasure:
		g := a.game
		g.phase, g.gift = "hide", rand.IntN(len(presents))
		g.origin = [2]int{explore.Block(a.pos.X), explore.Block(a.pos.Z)}
		prompt = fmt.Sprintf("Ты затеял игру «клад» для игроков рядом (%s): сейчас улетишь прятать настоящий клад и "+
			"вернёшься загадать, где он. Попроси не подглядывать и ждать тебя тут. Одна фраза до 140 символов, в "+
			"характере.", who)
		a.goTo(ctx, pl.X, pl.Z, "treasure: to the place")
	default:
		// The judge, not a racer: on an elytra it won every race.
		prompt = fmt.Sprintf("Ты объявляешь гонку для игроков рядом (%s): кто первым доберётся до финиша — %s, "+
			"на %s, примерно %d блоков. Ты будешь судьёй: сейчас улетишь к финишу и оттуда дашь старт ракетой и "+
			"криком «марш». Скажи, чтобы ждали сигнала. Одна-две фразы до 200 символов, в характере.", who, pl.Desc, dir, d)
		a.goTo(ctx, pl.X, pl.Z, "race: to the finish")
	}
	a.enqueue(replyJob{prompt: prompt, history: slices.Clone(a.history), at: time.Now()})
	return true
}

// riddlePrompt asks for the riddle about the game's place: the plain one, or where the treasure is.
func riddlePrompt(g *game, who string, d int, dir string) string {
	what, prize := "место", "первый, кто доберётся туда, получит от тебя подарок"
	if g.kind == gameTreasure {
		what = "место, где ты спрятал клад — " + presents[g.gift].say + ","
		prize = "клад достанется тому, кто первым его найдёт"
	}
	return fmt.Sprintf("Ты затеял игру для игроков рядом (%s): загадку-место. Загадай им %s так, чтобы его можно "+
		"было найти, но не сразу. Место: %s. Оно примерно в %d блоках от тебя, на %s. Не называй координаты и не "+
		"повторяй описание дословно. Скажи, что %s, а ты будешь подсказывать «горячо-холодно». Одна-две фразы до "+
		"200 символов, в характере.", who, what, g.place.Desc, d, dir, prize)
}

// hideTreasure: off to the place, the present dropped there, back to the players, and then the
// riddle as usual -- the treasure is the prize. A flight that does not get there in time, or a
// drop that fails (an older mod without dropItem), leaves the present to be handed over by @give.
func (a *Agent) hideTreasure(ctx context.Context, g *game, pos explore.Pos) {
	if a.flying || a.hovering {
		return
	}
	switch g.phase {
	case "hide":
		at := math.Hypot(pos.X-float64(g.place.X), pos.Z-float64(g.place.Z)) <= judgeNear
		if !at && time.Since(g.started) < raceSetup {
			return
		}
		if at {
			g.dropped = a.dropTreasure(ctx, g)
		}
		g.phase, g.backAt = "back", time.Now()
		a.goTo(ctx, g.origin[0], g.origin[1], "treasure: back")
	case "back":
		if math.Hypot(pos.X-float64(g.origin[0]), pos.Z-float64(g.origin[1])) > judgeNear &&
			time.Since(g.backAt) < raceSetup {
			return
		}
		g.phase, g.started, g.ahead = "", time.Now(), map[string]bool{}
		px, pz := float64(g.place.X), float64(g.place.Z)
		for _, p := range a.visiblePlayers(ctx) {
			if math.Hypot(p.x-px, p.z-pz) < raceAhead {
				g.ahead[p.name] = true // followed it there and watched: no fair find
			}
		}
		d, dir := a.placeDistDir(g.place)
		a.log.Info("treasure: search on", "dropped", g.dropped, "place", g.place.Desc, "not_searching", len(g.ahead))
		a.enqueue(replyJob{prompt: riddlePrompt(g, strings.Join(spokenNames(g.with), ", "), d, dir),
			history: slices.Clone(a.history), at: time.Now()})
	}
}

// dropTreasure leaves the game's present on the ground at the bot's feet (fork's dropItem).
func (a *Agent) dropTreasure(ctx context.Context, g *game) bool {
	gift := presents[g.gift]
	text, err := a.mc.Call(ctx, "dropItem", map[string]any{"item": gift.item, "count": gift.count})
	if err != nil {
		a.log.Warn("treasure: drop", "err", err)
		return false
	}
	var st struct {
		OK      bool `json:"ok"`
		Dropped int  `json:"dropped"`
	}
	ok := json.Unmarshal([]byte(text), &st) == nil && st.OK && st.Dropped > 0
	a.log.Info("treasure: hidden", "ok", ok, "item", gift.item, "result", text)
	return ok
}

// goTo flies to x, z, or walks there when it cannot fly.
func (a *Agent) goTo(ctx context.Context, x, z int, why string) {
	if a.startFlight(ctx, x, z) {
		return
	}
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", x, z)}); err != nil {
		a.log.Warn(why, "err", err)
	}
}

// checkGame judges a game under way: a winner, a clue, "warmer/colder", the race start, the end
// of time.
func (a *Agent) checkGame(ctx context.Context, pos explore.Pos) {
	g := a.game
	if g == nil {
		return
	}
	if g.kind == gameTreasure && g.phase != "" {
		a.hideTreasure(ctx, g, pos)
		return // hidden first: nobody wins before the riddle
	}
	px, pz := float64(g.place.X), float64(g.place.Z)
	if g.kind == gameRace && !g.running {
		atFinish := math.Hypot(pos.X-px, pos.Z-pz) <= judgeNear && !a.flying && !a.hovering
		if atFinish || time.Since(g.started) >= raceSetup {
			a.startRace(ctx, g, atFinish)
		}
		return // before the start nobody wins
	}
	players := a.visiblePlayers(ctx)
	for _, p := range players {
		if math.Hypot(p.x-px, p.z-pz) <= gameWinR && !g.ahead[p.name] {
			a.endGame(ctx, p.name)
			return
		}
	}
	limit := riddleFor
	if g.kind == gameRace {
		limit = raceFor
	}
	if time.Since(g.started) > limit {
		a.gameOver(ctx, g)
		return
	}
	if g.kind == gameRace {
		return
	}
	if !g.hinted && time.Since(g.started) > riddleHint {
		g.hinted = true
		d, dir := a.placeDistDir(g.place)
		what := "Твою загадку-место пока никто не разгадал"
		if g.kind == gameTreasure {
			what = "Твой клад пока никто не нашёл"
		}
		a.enqueue(replyJob{prompt: fmt.Sprintf("%s. Место: %s, на %s от тебя, около %d блоков. Дай вторую "+
			"подсказку, понятнее первой, одной фразой до 140 символов.", what, g.place.Desc, dir, d),
			history: slices.Clone(a.history), at: time.Now()})
		return
	}
	a.callHeat(ctx, g, players)
}

// startRace gives the start signal: a shout and a rocket from the finish (from wherever it got to,
// when the flight there took too long).
func (a *Agent) startRace(ctx context.Context, g *game, atFinish bool) {
	g.running, g.started, g.ahead = true, time.Now(), map[string]bool{}
	px, pz := float64(g.place.X), float64(g.place.Z)
	for _, p := range a.visiblePlayers(ctx) {
		// Already standing at the finish (followed the judge there): watching, not racing.
		if math.Hypot(p.x-px, p.z-pz) < raceAhead {
			g.ahead[p.name] = true
		}
	}
	a.log.Info("race: start", "at_finish", atFinish, "place", g.place.Desc, "not_racing", len(g.ahead))
	a.sayNow(ctx, "На старт… внимание… марш!")
	a.salute(ctx, 1) // seen from the start line, where the shout may not carry
}

// gameOver: time is up and nobody got there. A riddle's place is said plainly, and it flies there
// to light it up with a rocket.
func (a *Agent) gameOver(ctx context.Context, g *game) {
	a.game = nil
	a.log.Info("game: nobody won", "kind", g.kind, "place", g.place.Desc)
	if g.kind == gameRace {
		a.enqueue(replyJob{prompt: fmt.Sprintf("Гонка закончилась: никто не добрался до финиша — %s. Подшути "+
			"по-доброму и позови на реванш, одной-двумя фразами до 160 символов.", g.place.Desc),
			history: slices.Clone(a.history), at: time.Now()})
		return
	}
	d, dir := a.placeDistDir(g.place)
	a.beacon = &beacon{x: g.place.X, z: g.place.Z, desc: g.place.Desc, until: time.Now().Add(beaconWait)}
	a.goTo(ctx, g.place.X, g.place.Z, "beacon: to the place")
	what := "место, которое ты загадал"
	if g.kind == gameTreasure && g.dropped {
		what = "твой клад (" + presents[g.gift].say + ", он так и лежит там)"
	}
	// Said plainly what the place was: "раскрой ответ" alone once got a new riddle with the
	// answer "нос".
	a.enqueue(replyJob{prompt: fmt.Sprintf("Игра закончилась: никто не нашёл %s. Скажи прямо, где это было: %s — "+
		"на %s от тебя, около %d блоков, и что сейчас слетаешь туда и запустишь над ним ракету, чтобы все увидели. "+
		"Не загадывай новых загадок. Подшути по-доброму, одной-двумя фразами до 160 символов.", what, g.place.Desc, dir, d),
		history: slices.Clone(a.history), at: time.Now()})
}

// checkBeacon fires the rocket over a riddle's place once it is there.
func (a *Agent) checkBeacon(ctx context.Context, pos explore.Pos) {
	b := a.beacon
	if b == nil {
		return
	}
	if time.Now().After(b.until) {
		a.beacon = nil
		a.log.Info("beacon: did not get there", "place", b.desc)
		return
	}
	if a.flying || a.hovering || math.Hypot(pos.X-float64(b.x), pos.Z-float64(b.z)) > judgeNear+2 {
		return
	}
	if a.salute(ctx, 1) { // not on the ground yet: the next tick
		a.beacon = nil
		a.log.Info("beacon: lit", "place", b.desc)
	}
}

// Heat bands: how near a searcher is to the riddle's place.
const (
	heatCold = iota // further than 70 blocks
	heatCool        // within 70
	heatWarm        // within 35
	heatHot         // within 15
)

func heatBand(d float64) int {
	switch {
	case d <= 15:
		return heatHot
	case d <= 35:
		return heatWarm
	case d <= 70:
		return heatCool
	}
	return heatCold
}

// heatLines: what it calls out when a searcher moves into a band, warmer (true) or colder than
// before. Fixed lines, said at once: the model takes a second or more, and by then they have
// moved on.
var heatLines = map[bool]map[int][]string{
	true: {
		heatHot:  {"Горячо, %s! Совсем рядом!", "Ой-ой, горячо, %s! Вот-вот найдёшь!", "Горячо-горячо, %s!"},
		heatWarm: {"Теплее, %s, теплее!", "Тепло, %s! Правильно идёшь.", "О, теплеет, %s!"},
		heatCool: {"Чуть теплее, %s…", "Уже не так холодно, %s."},
	},
	false: {
		heatWarm: {"Остываешь, %s!", "Ой, холоднее, %s, вернись чуток!"},
		heatCool: {"Холоднее, %s…", "Эх, холоднее, %s."},
		heatCold: {"Холодно, %s, совсем не туда!", "Брр, холодно, %s!"},
	},
}

// callHeat says "warmer" or "colder" to a searcher who has just moved into another band; the one
// nearest the place first, one call at a time.
func (a *Agent) callHeat(ctx context.Context, g *game, players []seenPlayer) {
	if g.heat == nil {
		g.heat = map[string]int{}
	}
	px, pz := float64(g.place.X), float64(g.place.Z)
	slices.SortFunc(players, func(p, q seenPlayer) int {
		return int(math.Hypot(p.x-px, p.z-pz) - math.Hypot(q.x-px, q.z-pz))
	})
	quiet := time.Since(g.started) < heatQuiet || time.Since(g.lastHeat) < heatEvery
	for _, p := range players {
		band := heatBand(math.Hypot(p.x-px, p.z-pz))
		was, seen := g.heat[p.name]
		if !seen {
			g.heat[p.name] = band // the first look only sets where they start from
			continue
		}
		lines := heatLines[band > was][band]
		if quiet || band == was || len(lines) == 0 {
			continue // remembered as it was: the move is called out once it may speak
		}
		g.heat[p.name] = band
		g.lastHeat = time.Now()
		a.log.Info("riddle: heat", "player", p.name, "band", band, "was", was)
		a.sayNow(ctx, fmt.Sprintf(lines[rand.IntN(len(lines))], spokenName(p.name)))
		return
	}
}

// endGame congratulates the winner and hands over a present (in creative, from the creative tab).
func (a *Agent) endGame(ctx context.Context, winner string) {
	g := a.game
	a.game = nil
	a.log.Info("game: won", "kind", g.kind, "winner", winner, "place", g.place.Desc,
		"seconds", int(time.Since(g.started).Seconds()))
	if g.kind == gameTreasure && g.dropped {
		// The treasure is the present, and it is lying at their feet.
		a.mood("happy", 8)
		a.rememberWin(winner, "первым нашёл клад", g.place.Desc, presents[g.gift].say)
		a.enqueue(replyJob{prompt: fmt.Sprintf("Игрок %s первым нашёл твой клад (%s)! Поздравь его и скажи, что клад — "+
			"%s — теперь его, пусть подбирает. Одной-двумя фразами до 160 символов, в характере.",
			spokenName(winner), g.place.Desc, presents[g.gift].say),
			history: slices.Clone(a.history), at: time.Now()})
		return
	}
	gift := presents[rand.IntN(len(presents))]
	if g.kind == gameTreasure {
		gift = presents[g.gift] // the one it meant to hide
	}
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
	a.rememberWin(winner, kind, g.place.Desc, what)
	a.enqueue(replyJob{prompt: fmt.Sprintf("Игрок %s %s (%s)! Поздравь его и скажи, что несёшь ему подарок: %s. "+
		"Одной-двумя фразами до 160 символов, в характере.", spokenName(winner), kind, g.place.Desc, what),
		history: slices.Clone(a.history), at: time.Now()})
}

// rememberWin notes the win in what the bot remembers about the winner.
func (a *Agent) rememberWin(winner, how, place, gift string) {
	if a.people == nil {
		return
	}
	if err := a.people.Append(winner, "(в игре "+how+": "+place+")", "подарил: "+gift); err != nil {
		a.log.Warn("people memory", "err", err)
	}
}

// judging: a game is under way (or its beacon is due), and where the bot stands is part of it --
// the riddle's clues say where the place is from it, the race's judge waits at the finish. So it
// keeps its place: no shows, outings or flights after others till it is over.
func (a *Agent) judging() bool { return a.game != nil || a.beacon != nil }

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
