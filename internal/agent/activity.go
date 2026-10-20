package agent

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/alteralto/persona-agent/internal/explore"
)

// activity is something the bot does on its own initiative, independent of how chatty the
// others are: idle-only behaviour never fired while two other bots talked non-stop.
type activity struct {
	kind    string // "sign" | "scout" | "visit" | "mark" | "stroll"
	x, z    int
	label   string
	started time.Time
}

const activityTimeout = 90 * time.Second

// maybeActivity starts an activity when nobody has asked the bot for anything lately.
func (a *Agent) maybeActivity(ctx context.Context) bool {
	// retryFlight: walking out from under a roof to take off again; a new plan now would cancel it.
	if a.cfg.ActivityEvery <= 0 || a.outing != nil || a.activity != nil || a.flying || a.trip != nil || !a.havePos ||
		a.retryFlight != nil {
		return false
	}
	// "Стой тут" holds the bot for a minute: no flight shows in the meantime.
	if time.Since(a.lastRequest) < 30*time.Second {
		return false
	}
	// Judging a riddle: it stays where it set it ("на юго-востоке от меня" holds); talk only.
	if a.judging() {
		return a.mingle(ctx)
	}
	// Staying a while next to people (a visit, a place, a sign): not just standing there.
	if time.Since(a.lastActivity) < a.cfg.ActivityEvery || time.Now().Before(a.lingerUntil) {
		return a.mingle(ctx)
	}
	a.lastActivity = time.Now()
	// FlightShare > 0 (flight testing): that share of activities are flights, the rest as usual.
	if a.cfg.FlightShare > 0 && rand.Float64() < a.cfg.FlightShare && a.startFlightShow(ctx) {
		return true
	}
	// Players on the server but none close: go over to the nearest one (not a companion: a
	// visit, not a follow).
	if a.startVisit(ctx) {
		return true
	}
	// Now and then a fireworks show for whoever is around (creative only).
	if a.othersNear > 0 && rand.IntN(5) == 0 && time.Since(a.lastRocketShow) > rocketShowEvery && a.startSaluteShow(ctx) {
		return true
	}
	// Shares: outing, signs, scouting, flight. Flying is the show people stop to watch, so in
	// creative it gets the biggest share; signs are quiet and were most of the day before.
	shares := [4]float64{0.35, 0.30, 0.20, 0.15}
	if a.creative {
		shares = [4]float64{0.20, 0.10, 0.20, 0.50}
	}
	r := rand.Float64()
	switch {
	case r < shares[0] && a.startOuting(ctx):
		return true
	case r >= shares[0] && r < shares[0]+shares[1] && a.startSignTour(ctx):
		return true
	case r >= shares[0]+shares[1] && r < 1-shares[3] && a.startScout(ctx):
		return true
	case r >= 1-shares[3] && a.startFlightShow(ctx):
		return true
	}
	// The drawn one was not possible (no signs left, no places yet): try the others, and at
	// worst a stroll -- standing about for half a minute at a time looks broken.
	return a.startSignTour(ctx) || a.startOuting(ctx) || a.startScout(ctx) || a.startStroll(ctx)
}

// startStroll walks 12-25 blocks: back towards home when it has wandered off, else anywhere.
func (a *Agent) startStroll(ctx context.Context) bool {
	angle := rand.Float64() * 2 * math.Pi
	if h := a.places.Home; h != nil && math.Hypot(h.X-a.pos.X, h.Z-a.pos.Z) > 60 {
		angle = math.Atan2(h.Z-a.pos.Z, h.X-a.pos.X) + (rand.Float64()-0.5)*0.8
	}
	d := 12 + rand.Float64()*13
	x, z := explore.Block(a.pos.X+d*math.Cos(angle)), explore.Block(a.pos.Z+d*math.Sin(angle))
	if !a.walkTo(ctx, x, z) {
		return false
	}
	a.activity = &activity{kind: "stroll", x: x, z: z, started: time.Now()}
	a.log.Info("activity: stroll", "x", x, "z", z)
	return true
}

// startSignTour walks to the nearest sign not read yet.
func (a *Agent) startSignTour(ctx context.Context) bool {
	var best *sign
	for _, s := range a.signsNear(ctx, 48) {
		s := s
		text := signText(s)
		if text == "" || mostlyNumbers(text) || s.Distance < 4 || a.readSigns[s.Pos] {
			continue
		}
		best = &s
		break // nearest first
	}
	if best == nil {
		return false
	}
	x, z, ok := parseBlockPos(best.Pos)
	if !ok {
		return false
	}
	if !a.walkTo(ctx, x, z) {
		return false
	}
	a.activity = &activity{kind: "sign", x: x, z: z, label: signText(*best), started: time.Now()}
	a.readSigns[best.Pos] = true
	a.log.Info("activity: reading a sign", "text", a.activity.label, "x", x, "z", z)
	return true
}

// startScout walks to an unvisited chunk next to the bot, to find new places and signs.
func (a *Agent) startScout(ctx context.Context) bool {
	cur := explore.ChunkOf(a.pos)
	// Around home, not around wherever the bot is: scouting "next door" each time walked it
	// ever further away -- into a lake, last time.
	center, radius := cur, 2
	if a.places.Home != nil {
		center, radius = explore.ChunkOf(*a.places.Home), max(a.cfg.ExploreRadius, 4)
	}
	next, ok := explore.Next(center, cur, radius, a.places.VisitedSet())
	if !ok {
		return false
	}
	x, z := next.Center()
	if !a.walkTo(ctx, x, z) {
		return false
	}
	a.activity = &activity{kind: "scout", x: x, z: z, started: time.Now()}
	a.log.Info("activity: scouting", "x", x, "z", z)
	return true
}

// startFlightShow takes off for a far known place and says so.
func (a *Agent) startFlightShow(ctx context.Context) bool {
	style := a.pickStyle()
	if !a.flyShowOff(ctx, style) {
		return false
	}
	a.log.Info("activity: flight", "style", style)
	how := map[string]string{
		styleNormal: "надеваешь элитры и взлетаешь на ракетах, хоть и боишься высоты",
		styleLow:    "надеваешь элитры и летишь бреющим, в паре блоков над землёй — лихо и страшно",
		styleTricks: "взлетаешь и будешь крутить фигуры в небе — бочку, свечу, спираль; зажмуриваешься от страха",
		styleRoute:  "облетаешь окрестности по кругу и вернёшься к норе — смотришь на всё сверху",
	}[style]
	a.announceTakeoff("Ты сам решил полетать: "+how+". Скажи об этом одной короткой фразой до 90 символов, в характере.", nil)
	return true
}

// startSaluteShow launches a fireworks show on the bot's own and says so.
func (a *Agent) startSaluteShow(ctx context.Context) bool {
	if !a.salute(ctx, 1) {
		return false
	}
	a.log.Info("activity: salute")
	a.enqueue(replyJob{
		prompt: "Ты сам решил запустить одну красивую ракету с цветными звёздами — для тех, кто рядом. " +
			"Скажи об этом одной короткой фразой до 90 символов, в характере.",
		at: time.Now(),
	})
	return true
}

// checkActivity runs while an activity is under way.
func (a *Agent) checkActivity(ctx context.Context, pos explore.Pos) {
	act := a.activity
	if math.Hypot(pos.X-float64(act.x), pos.Z-float64(act.z)) < 4.5 {
		a.activity = nil
		a.lingerFor(20 * time.Second) // a moment at the spot before drifting back
		switch act.kind {
		case "sign":
			a.places.Add(explore.Place{Desc: "табличка «" + act.label + "»", X: act.x, Z: act.z, Seen: time.Now()})
			a.savePlaces()
			a.enqueue(replyJob{
				prompt: fmt.Sprintf("Ты подошёл к табличке, на ней написано: «%s». Прочитай её вслух своими "+
					"словами и коротко прокомментируй, в характере. Одна-две фразы до 120 символов.", act.label),
				history: append([]string(nil), a.history...),
				at:      time.Now(),
			})
		case "scout":
			a.survey(ctx, pos, explore.ChunkOf(pos))
		case "mark":
			a.lingerFor(markStay) // stay at the mark a while
			a.celebrate(ctx, "mark")
		case "visit":
			a.lingerFor(visitStay) // stay near them a while, doing its own things
			a.celebrate(ctx, "visit")
			if time.Since(a.greeted[act.label]) > time.Hour {
				a.greeted[act.label] = time.Now()
				a.enqueue(replyJob{
					prompt: fmt.Sprintf("Ты сам подошёл поближе к игроку %s, он в десятке блоков от тебя. "+
						"Поздоровайся с ним по имени коротко и ненавязчиво, в характере. Одна фраза до 90 символов.",
						spokenName(act.label)),
					history: append([]string(nil), a.history...),
					at:      time.Now(),
				})
			}
		}
		return
	}
	limit := activityTimeout
	if act.kind == "mark" {
		limit = markTimeout
	}
	if time.Since(act.started) > limit {
		a.log.Info("activity: gave up", "kind", act.kind)
		a.activity = nil
		a.dropScoutTarget(act)
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
			a.log.Warn("activity stop", "err", err)
		}
	}
}

// rocketShowEvery: at most one pretty rocket from the ground this often, unless asked for a show.
// One goes up after a bright moment (a landing, a visit, a mark reached) and about every minute
// while people are around.
const rocketShowEvery = time.Minute

const (
	visitNear  = 30.0             // someone this close: the bot is among people already
	visitShort = 12.0             // stop this far from the player, not on top of them
	visitAgain = 5 * time.Minute  // the same player at most this often
	visitStay  = 90 * time.Second // then stay near them a while
)

// startVisit goes over to the nearest player in sight when nobody is close. It is a visit, not
// a follow: the bot stops short, stays a while, and lets them go where they like.
func (a *Agent) startVisit(ctx context.Context) bool {
	if a.visited == nil {
		a.visited, a.greeted = map[string]time.Time{}, map[string]time.Time{}
	}
	var best *seenPlayer
	for _, p := range a.visiblePlayers(ctx) {
		if p.dist <= visitNear {
			return false // already among people
		}
		if time.Since(a.visited[p.name]) < visitAgain {
			continue
		}
		if best == nil || p.dist < best.dist {
			best = &p
		}
	}
	if best == nil || !a.havePos {
		return false
	}
	k := (best.dist - visitShort) / best.dist
	x := explore.Block(a.pos.X + (best.x-a.pos.X)*k)
	z := explore.Block(a.pos.Z + (best.z-a.pos.Z)*k)
	if !a.walkTo(ctx, x, z) {
		return false
	}
	a.visited[best.name] = time.Now()
	a.partner, a.partnerAt = best.name, time.Now()
	a.activity = &activity{kind: "visit", x: x, z: z, label: best.name, started: time.Now()}
	a.log.Info("activity: visiting", "player", best.name, "dist", int(best.dist))
	return true
}

func (a *Agent) walkTo(ctx context.Context, x, z int) bool {
	a.following = ""
	if math.Hypot(float64(x)-a.pos.X, float64(z)-a.pos.Z) > a.flyAbove() && a.startFlight(ctx, x, z) {
		return true
	}
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", x, z)}); err != nil {
		a.log.Warn("activity goto", "err", err)
		return false
	}
	return true
}

func signText(s sign) string {
	t := strings.TrimSpace(s.Front)
	if t == "" {
		t = strings.TrimSpace(s.Back)
	}
	return t
}

// parseBlockPos reads "x,y,z" as printed by readSigns.
func parseBlockPos(s string) (int, int, bool) {
	p := strings.Split(s, ",")
	if len(p) != 3 {
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(strings.TrimSpace(p[0]))
	z, err2 := strconv.Atoi(strings.TrimSpace(p[2]))
	return x, z, err1 == nil && err2 == nil
}

// dropScoutTarget marks a scout target the bot could not reach (a lake, a cliff) as seen, so the
// next scout picks another one: it went back to the same lake chunk again and again.
func (a *Agent) dropScoutTarget(act *activity) {
	if act == nil || act.kind != "scout" {
		return
	}
	a.places.MarkVisited(explore.ChunkOf(explore.Pos{X: float64(act.x), Z: float64(act.z)}))
	a.savePlaces()
}

// mingleEvery: while he stays among people, one small thing of his own this often.
const mingleEvery = 25 * time.Second

// mingleTopics: what he starts a talk with, so the lines do not all come out the same.
var mingleTopics = []string{
	"спроси, что он сейчас делает или строит, и похвали по-хоббитски",
	"позови его на чай в нору или угости чем-нибудь из своих запасов",
	"предложи ему ракет для элитр — ты ведь наземный техник",
	"расскажи коротко про место, которое ты недавно видел, и предложи показать",
	"пожалуйся в шутку на что-нибудь хоббитское: на сквозняк, на криперов, на пустой чайник",
	"спроси его совета по хозяйству: где взять бумагу, порох или хорошую еду",
	"похвастайся, что научился летать на элитрах, и спроси, хочет ли он посмотреть фигуры",
	"задай ему смешной вопрос о нём самом, по-соседски",
}

// mingle does one small thing with the players next to him: talks to the nearest one, steps
// closer, or (in creative, when nobody asked him to stand still) puts on a little show.
func (a *Agent) mingle(ctx context.Context) bool {
	if a.othersNear == 0 || time.Since(a.lastMingle) < mingleEvery ||
		time.Since(time.Unix(0, a.lastSpoke.Load())) < 12*time.Second {
		return false
	}
	var near *seenPlayer
	for _, p := range a.visiblePlayers(ctx) {
		if p.dist <= visitNear && (near == nil || p.dist < near.dist) {
			near = &p
		}
	}
	if near == nil {
		return false
	}
	a.lastMingle = time.Now()
	a.partner, a.partnerAt = near.name, time.Now()
	// "Стой тут" a minute ago, or judging a riddle: words only.
	free := time.Since(a.lastRequest) > 90*time.Second && !a.judging()
	r := rand.Float64()
	switch {
	case free && !a.cfg.NoOwnGames && r < 0.08 && time.Since(a.lastGame) > gameEvery && a.startGame(ctx, []string{gameRiddle, gameRace}[rand.IntN(2)], ""):
		a.log.Info("mingle: game", "player", near.name)
		return true
	case free && a.creative && r < 0.15 && a.startFlightShow(ctx):
		a.log.Info("mingle: show", "player", near.name)
		return true
	case free && a.creative && r < 0.55 && time.Since(a.lastRocketShow) > rocketShowEvery && a.startSaluteShow(ctx):
		a.log.Info("mingle: rocket", "player", near.name)
		return true
	case free && r < 0.40 && near.dist > 6 && a.following == "": // a walk over would end the follow
		k := (near.dist - 3.5) / near.dist
		x, z := explore.Block(a.pos.X+(near.x-a.pos.X)*k), explore.Block(a.pos.Z+(near.z-a.pos.Z)*k)
		if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": fmt.Sprintf("@goto %d %d", x, z)}); err == nil {
			a.log.Info("mingle: steps closer", "player", near.name)
		}
	}
	i := rand.IntN(len(mingleTopics) - 1)
	if i >= a.lastTopicMingle { // never the same topic twice running
		i++
	}
	a.lastTopicMingle = i
	topic := mingleTopics[i]
	a.log.Info("mingle: talks", "player", near.name, "topic", topic)
	a.lastSpoke.Store(time.Now().UnixNano())
	a.enqueue(replyJob{
		prompt: fmt.Sprintf("Рядом с тобой игрок %s, в %d блоках. Заговори с ним сам, по имени: %s. "+
			"Одна живая фраза до 90 символов, в характере, не повторяй то, что уже говорил.",
			spokenName(near.name), int(near.dist), topic),
		history: append([]string(nil), a.history...),
		at:      time.Now(),
	})
	return true
}

// celebrate sends up one pretty rocket for a bright moment, at most every rocketShowEvery. A
// rocket stops the bot to aim at the ground, so not while it walks somewhere: a rocket for joy
// must not cost the walk.
func (a *Agent) celebrate(ctx context.Context, why string) {
	if !a.creative || time.Since(a.lastRocketShow) < rocketShowEvery ||
		a.trip != nil || a.outing != nil || a.activity != nil || a.leading || a.retryFlight != nil {
		return
	}
	if a.salute(ctx, 1) {
		a.log.Info("celebrate", "why", why)
	}
}

// mostlyNumbers: a scoreboard or an address plate ("№ 11 Участник #64444063 10 баллов"), dull to
// read aloud: more digits than letters, or a long number (an id, a phone).
func mostlyNumbers(text string) bool {
	digits, letters, run := 0, 0, 0
	for _, r := range text {
		if unicode.IsDigit(r) {
			digits++
			if run++; run >= 5 {
				return true
			}
			continue
		}
		run = 0
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return digits > letters
}
