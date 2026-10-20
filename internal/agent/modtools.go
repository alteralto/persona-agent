package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

// Tools from our UnionClef fork (setBuildPolicy, readSigns, creativeGive, flyTo, gameMode in
// getGameState). With the upstream mod they are missing and every helper here falls back to
// the old behaviour, so one agent build works with either jar.

func unknownTool(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Unknown tool")
}

// protectBuilds asks the pathfinder to place nothing and, unless AllowBreak, to mine nothing at
// all (else only natural terrain). With the upstream mod, or a fork jar too old to forbid mining,
// it falls back to a no-break zone around home.
func (a *Agent) protectBuilds(ctx context.Context) {
	out, err := a.mc.Call(ctx, "setBuildPolicy", map[string]any{"breakOnlyNatural": true, "allowPlace": false,
		"allowBreak": a.cfg.AllowBreak})
	if err == nil && !a.cfg.AllowBreak && !strings.Contains(out, `"allowBreak":false`) {
		a.log.Warn("fork jar cannot forbid mining yet: a no-break zone around home instead", "result", out)
		a.fork = true
		a.protectHome(ctx)
		return
	}
	if err == nil {
		// The policy replaces the zone: drop zones a previous agent run left behind.
		if _, err := a.mc.Call(ctx, "clearProtectedAreas", nil); err != nil {
			a.log.Warn("clear protected areas", "err", err)
		}
		a.protected, a.fork = true, true
		a.log.Info("build policy set", "result", out)
		return
	}
	if !unknownTool(err) {
		a.log.Warn("build policy", "err", err)
		return
	}
	a.protectHome(ctx)
}

// refreshGameMode reads creative/survival from getGameState (fork only); falls back to config.
func (a *Agent) refreshGameMode(ctx context.Context) {
	text, err := a.mc.Call(ctx, "getGameState", nil)
	if err != nil {
		return
	}
	var st struct {
		Self struct {
			GameMode string `json:"gameMode"`
		} `json:"self"`
		Players []struct {
			Name string `json:"name"`
		} `json:"players"`
	}
	if json.Unmarshal([]byte(text), &st) != nil || st.Self.GameMode == "" {
		return
	}
	// Which of the other bots are actually around: inviting Pon4ikYT when he is not on the
	// server made the bot talk to nobody.
	a.botsNear, a.botsKnown, a.othersNear = nil, true, len(st.Players)
	for _, p := range st.Players {
		for _, b := range a.cfg.OtherBots {
			if strings.EqualFold(p.Name, b) {
				a.botsNear = append(a.botsNear, b)
			}
		}
	}
	creative := st.Self.GameMode == "creative"
	if st.Self.GameMode != a.gameMode || !a.modeKnown {
		a.log.Info("game mode", "mode", st.Self.GameMode)
	}
	a.creative, a.modeKnown, a.gameMode = creative, true, st.Self.GameMode

	// On our own server the bot plays in creative: ask for it when the server put us in survival.
	if a.cfg.AutoCreative && st.Self.GameMode == "survival" && time.Since(a.creativeAsked) > time.Minute {
		a.creativeAsked = time.Now()
		if _, err := a.mc.Call(ctx, "ChatMessage", map[string]any{"msg": a.cfg.CreativeCommand}); err != nil {
			a.log.Warn("creative command", "err", err)
		} else {
			a.log.Info("asked for creative", "cmd", a.cfg.CreativeCommand)
		}
	}
	a.applyFallSaves(ctx)
	a.tidyInventory(ctx)
}

// nearbyBots are the other bots in sight. Until the player list has been read (just after
// start or a game restart, or with the upstream mod) nobody: better silent than talking to an
// absent bot.
func (a *Agent) nearbyBots() []string {
	if !a.botsKnown {
		return nil
	}
	return a.botsNear
}

// companion is the first configured companion in sight, or "".
func (a *Agent) companion() string {
	for _, c := range a.cfg.Companions {
		if !a.botsKnown || slices.ContainsFunc(a.botsNear, func(b string) bool { return strings.EqualFold(b, c) }) {
			return c
		}
	}
	return ""
}

// tidyInventory keeps a few slots free (fork only). A full inventory blocked creativeGive and
// made the mod fire rockets it could not move out of the hand.
func (a *Agent) tidyInventory(ctx context.Context) {
	if !a.fork || a.noTidy || a.flying {
		return
	}
	text, err := a.mc.Call(ctx, "tidyInventory", map[string]any{"keepFree": 4})
	if err != nil {
		if unknownTool(err) {
			a.noTidy = true // an older fork jar
		} else {
			a.log.Warn("tidy inventory", "err", err)
		}
		return
	}
	var r struct {
		Freed int `json:"freed"`
		Free  int `json:"free"`
	}
	if json.Unmarshal([]byte(text), &r) == nil && r.Freed > 0 {
		a.log.Info("inventory tidied", "freed", r.Freed, "free", r.Free)
	}
}

// applyFallSaves sets the mod's fall rescue (water bucket, ender pearl toward the nearest
// player, blocks under the feet): useful in survival, pointless in creative, and harmful in
// flight, where a glide down looks like a fall and the bot throws pearls mid-air.
func (a *Agent) applyFallSaves(ctx context.Context) {
	if !a.modeKnown {
		return // upstream mod: mode unknown, leave the setting as it is
	}
	want := !a.creative && !a.flying
	if a.fallSavesSet && a.fallSaves == want {
		return
	}
	cmd := fmt.Sprintf("@set autoMLGBucket %t", want)
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": cmd}); err != nil {
		a.log.Warn("fall saves", "err", err)
		return
	}
	a.fallSaves, a.fallSavesSet = want, true
	a.log.Info("fall saves", "on", want)
}

type sign struct {
	Pos      string  `json:"pos"`
	Front    string  `json:"front"`
	Back     string  `json:"back"`
	Distance float64 `json:"distance"`
}

// signsNear returns sign texts within radius, nearest first; nil with the upstream mod.
func (a *Agent) signsNear(ctx context.Context, radius int) []sign {
	text, err := a.mc.Call(ctx, "readSigns", map[string]any{"radius": radius})
	if err != nil {
		if !unknownTool(err) {
			a.log.Warn("read signs", "err", err)
		}
		return nil
	}
	var out []sign
	_ = json.Unmarshal([]byte(text), &out)
	return out
}

// signName turns the nearest sign into a place name: "«Розовый дом»".
func signName(signs []sign) string {
	for _, s := range signs {
		t := strings.TrimSpace(s.Front)
		if t == "" {
			t = strings.TrimSpace(s.Back)
		}
		if t != "" {
			if r := []rune(t); len(r) > 40 {
				t = string(r[:40])
			}
			return "«" + t + "»"
		}
	}
	return ""
}

// creativeGive takes items from the creative inventory (fork, creative mode only).
func (a *Agent) creativeGive(ctx context.Context, item string, count int) bool {
	text, err := a.mc.Call(ctx, "creativeGive", map[string]any{"item": item, "count": count})
	if err != nil {
		a.log.Warn("creative give", "item", item, "err", err)
		return false
	}
	var r struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(text), &r)
	if !r.OK {
		a.log.Info("creative give refused", "item", item, "reason", r.Reason)
	}
	return r.OK
}

// startFlight sends the bot flying to (x,z) on an elytra. In creative it first takes an elytra
// and fireworks from the creative inventory (once per run). Returns false when flight is not
// possible, and the caller walks instead.
func (a *Agent) startFlight(ctx context.Context, x, z int) bool {
	return a.startFlightOpts(ctx, x, z, flightOpts{})
}

// Flight styles: how a show flight is flown.
const (
	styleNormal = "normal"
	styleLow    = "low"    // бреющий: a few blocks over the ground
	styleTricks = "tricks" // пилотаж: figures on the way
	styleRoute  = "route"  // облёт: through several places without landing
)

// flightOpts: low-level, and how many figures on the way. An older fork jar ignores both and
// flies the usual way.
type flightOpts struct {
	low    bool
	tricks int
}

func (o flightOpts) args(m map[string]any) map[string]any {
	if o.low {
		m["low"] = true
	}
	if o.tricks > 0 {
		m["tricks"] = o.tricks
	}
	return m
}

// startFlightOpts takes off for x, z.
func (a *Agent) startFlightOpts(ctx context.Context, x, z int, o flightOpts) bool {
	y := 70
	if a.places != nil && a.places.Home != nil {
		y = explore.Block(a.places.Home.Y)
	}
	a.lastOpts = o // a take-off that fails under a roof is retried in the same style
	if !a.takeOff(ctx, "flyTo", o.args(map[string]any{"x": x, "y": y, "z": z})) {
		return false
	}
	a.trackStyle = styleNormal
	if o.low {
		a.trackStyle = styleLow
	} else if o.tricks > 0 {
		a.trackStyle = styleTricks
	}
	a.flyX, a.flyZ, a.flyPoint = x, z, true
	a.log.Info("flight: taking off", "x", x, "z", z, "low", o.low, "tricks", o.tricks)
	return true
}

// startRoute flies through the points without landing and lands at the last (fork flyRoute).
func (a *Agent) startRoute(ctx context.Context, pts [][2]int, o flightOpts) bool {
	parts := make([]string, len(pts))
	for i, p := range pts {
		parts[i] = fmt.Sprintf("%d,%d", p[0], p[1])
	}
	if !a.takeOff(ctx, "flyRoute", o.args(map[string]any{"points": strings.Join(parts, ";")})) {
		last := pts[len(pts)-1] // an older jar has no routes: fly to the end of it
		return a.startFlightOpts(ctx, last[0], last[1], o)
	}
	last := pts[len(pts)-1]
	a.trackStyle = styleRoute
	a.flyX, a.flyZ, a.flyPoint = last[0], last[1], true
	a.log.Info("flight: route", "points", strings.Join(parts, ";"))
	return true
}

// pickStyle: FLIGHT_STYLE when set, else a mix -- mostly plain flights, now and then the others.
func (a *Agent) pickStyle() string {
	switch a.cfg.FlightStyle {
	case styleNormal, styleLow, styleTricks, styleRoute:
		return a.cfg.FlightStyle
	}
	// Low flight is the one that looks best from the ground: the biggest share.
	switch r := rand.IntN(20); {
	case r < 5:
		return styleNormal
	case r < 12:
		return styleLow
	case r < 17:
		return styleTricks
	default:
		return styleRoute
	}
}

// startWingman takes off to fly beside a player who is flying (fork's flyFollow); after they
// land the bot follows them on foot again.
func (a *Agent) startWingman(ctx context.Context, nick string) bool {
	if !a.takeOff(ctx, "flyFollow", map[string]any{"nick": nick}) {
		return false
	}
	a.followAfter = nick
	a.log.Info("flight: wingman", "leader", nick)
	return true
}

// takeOff readies the flight kit and calls a fork flight tool (flyTo or flyFollow). False when
// flight is not possible, and the caller walks instead.
func (a *Agent) takeOff(ctx context.Context, tool string, args map[string]any) bool {
	if a.noFlight || !a.fork { // upstream mod: no flyTo, no creativeGive
		return false
	}
	// A take-off just failed (in water, under a roof): walk out instead of jumping again.
	if time.Since(a.flightFailedAt) < time.Minute {
		return false
	}
	if a.creative && !a.flightKit {
		a.tidyInventory(ctx)
		a.flightKit = a.creativeGive(ctx, "elytra", 1) && a.creativeGive(ctx, "firework_rocket", 64)
		// A sword too: mob defense takes the best weapon carried, else it fights bare-handed.
		a.creativeGive(ctx, "netherite_sword", 1)
	}
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
		a.log.Warn("flight stop walk", "err", err)
	}
	a.following = "" // the @stop above ends any follow; callers set followAfter to resume it
	a.flying = true  // fall saves off before take-off: a glide must not trigger pearl throws
	a.applyFallSaves(ctx)
	if _, err := a.mc.Call(ctx, tool, args); err != nil {
		if unknownTool(err) && tool == "flyTo" {
			a.noFlight = true // upstream mod: never try again this run
		} else if unknownTool(err) {
			a.log.Info("fork jar without " + tool + ": a plain flight instead")
		} else {
			a.log.Warn("fly", "err", err)
		}
		a.flying = false
		a.applyFallSaves(ctx)
		return false
	}
	a.flying, a.flightStarted = true, time.Now()
	a.flyPoint = false // beside a player; startFlightOpts and startRoute set it for a point
	a.track = nil
	a.mood("scared", 30) // he is afraid of heights
	return true
}

// takeoffSayGap: a take-off is announced at most this often. In creative half its activities are
// flights, and a line for each came out as "Опять… держись крепче" four times in five minutes.
const takeoffSayGap = 3 * time.Minute

// announceTakeoff asks for a line about going up, unless it said one lately: then it just flies.
func (a *Agent) announceTakeoff(prompt string, history []string) {
	if time.Since(a.lastTakeoffSay) < takeoffSayGap {
		a.log.Info("flight: quiet take-off")
		return
	}
	a.lastTakeoffSay = time.Now()
	a.enqueue(replyJob{prompt: prompt, history: history, takeoff: true, at: time.Now()})
}

// takeoffNote shows the model its earlier take-off lines: the general list of its last lines has
// moved on by the next flight, and on its own the same prompt gives the same opening every time.
func takeoffNote(said []string) string {
	if len(said) == 0 {
		return ""
	}
	return "Так ты уже говорил о своих взлётах — теперь скажи совсем иначе: другое начало, другие слова и " +
		"мысль, не «опять» и не «держись крепче»:\n- " + strings.Join(said, "\n- ") + "\n\n"
}

// mood sets the avatar's face for a while; nothing without the voice service.
func (a *Agent) mood(name string, seconds int) {
	if a.speaker != nil {
		go a.speaker.Mood(context.Background(), name, seconds)
	}
}

// Ways a player is up in the air.
const (
	airGlide = "glide" // on an elytra
	airHover = "hover" // creative flight: hangs, stops, turns on the spot
)

// flyingPlayer is someone in the air near the bot.
type flyingPlayer struct {
	name string
	how  string
	dist float64
}

// playersInAir lists the players flying now, nearest first (the mod sorts them).
func (a *Agent) playersInAir(ctx context.Context) []flyingPlayer {
	text, err := a.mc.Call(ctx, "getGameState", nil)
	if err != nil {
		return nil
	}
	var st struct {
		Players []struct {
			Name     string `json:"name"`
			Distance string `json:"distance"`
			Gliding  bool   `json:"gliding"`
			Hovering bool   `json:"hovering"`
			Pos      string `json:"pos"`
		} `json:"players"`
	}
	if json.Unmarshal([]byte(text), &st) != nil {
		return nil
	}
	var out []flyingPlayer
	for _, p := range st.Players {
		d, _ := strconv.ParseFloat(p.Distance, 64)
		switch {
		case p.Gliding:
			out = append(out, flyingPlayer{p.Name, airGlide, d})
		case p.Hovering && !a.parkedInAir(p.Name, p.Pos, time.Now()):
			out = append(out, flyingPlayer{p.Name, airHover, d})
		}
	}
	return out
}

// hoverMark is where a creative flyer was when last seen moving.
type hoverMark struct {
	x, y, z float64
	at      time.Time
}

// parkedAfter: a creative flyer who has stayed within parkedRadius this long is standing in the
// air (building, AFK), not flying -- nothing to circle round, or the bot orbits them for ever.
const (
	parkedAfter  = 12 * time.Second
	parkedRadius = 3.0
)

// parkedInAir says whether a hovering player has been hanging in one place for a while (or is
// seen for the first time, so nobody knows yet), and remembers where they were seen moving last.
func (a *Agent) parkedInAir(name, pos string, now time.Time) bool {
	p, err := explore.ParsePos(pos)
	if err != nil {
		return false
	}
	x, y, z := p.X, p.Y, p.Z
	if a.hoverMarks == nil {
		a.hoverMarks = map[string]hoverMark{}
	}
	m, seen := a.hoverMarks[name]
	if !seen {
		// Not known yet whether they are going anywhere: wait for a second look.
		a.hoverMarks[name] = hoverMark{x, y, z, now}
		return true
	}
	if math.Hypot(math.Hypot(x-m.x, z-m.z), y-m.y) > parkedRadius {
		a.hoverMarks[name] = hoverMark{x, y, z, now}
		return false
	}
	return now.Sub(m.at) >= parkedAfter
}

// flyTogether: how near a flying player must be for the bot to join them, and how long after
// one flight together it waits before it joins anyone again (not glued to every flyer).
const (
	flyTogetherNear  = 40.0
	flyTogetherAgain = 90 * time.Second
)

// joinFlyers takes off beside a player in the air: the one it follows at once, anyone close by
// now and then. An elytra flyer gets an elytra wingman; a creative flyer, creative flight.
func (a *Agent) joinFlyers(ctx context.Context) bool {
	if !a.fork || a.flying || a.hovering || a.retryFlight != nil || a.judging() {
		return false
	}
	// Whom to go up with: the one it follows; else the one it is with right now (visiting,
	// talking to); else the nearest. One at a time -- two flying apart, it stays with its choice.
	var pick *flyingPlayer
	mine := false
	partner := a.partner != "" && time.Since(a.partnerAt) < 3*time.Minute
	for _, p := range a.playersInAir(ctx) {
		if a.following != "" && strings.EqualFold(p.name, a.following) {
			pick, mine = &p, true
			break
		}
		if p.dist > flyTogetherNear || time.Since(a.lastFlyTogether) < flyTogetherAgain ||
			slices.ContainsFunc(a.cfg.OtherBots, func(b string) bool { return strings.EqualFold(b, p.name) }) {
			continue
		}
		if pick == nil || partner && strings.EqualFold(p.name, a.partner) {
			pick = &p
		}
	}
	if pick == nil {
		return false
	}
	p := *pick
	// Not obliged: a hobbit afraid of heights goes up with a companion, with anyone else about
	// every other time, and says so either way.
	if !mine && rand.IntN(2) == 0 {
		a.lastFlyTogether = time.Now() // no asking itself again every two seconds
		a.log.Info("flight: stays on the ground", "flyer", p.name)
		a.enqueue(replyJob{
			prompt: fmt.Sprintf("Игрок %s взлетел рядом с тобой. Ты решил не лететь следом — высоты боишься, "+
				"лучше снизу посмотришь или помашешь. Скажи ему об этом одной короткой фразой до 90 символов, в характере.",
				spokenName(p.name)),
			history: append([]string(nil), a.history...),
			at:      time.Now(),
		})
		return false
	}
	// On an elytra whichever way they fly: beside a glider, circling round a creative flyer
	// (the pilot orbits a slow leader). Creative flight only when the elytra cannot go up.
	ok := !a.noFlight && a.startWingman(ctx, p.name)
	if !ok && p.how == airHover && a.creative {
		ok = a.startHover(ctx, p.name)
	}
	if !ok {
		return false
	}
	a.lastFlyTogether = time.Now()
	a.trip, a.outing, a.activity, a.leading = nil, nil, nil, false
	a.mood("scared", 20)
	a.announceTakeoff(fmt.Sprintf("Игрок %s полетел, и ты сам взлетаешь следом, чтобы лететь рядом с ним. "+
		"Скажи ему об этом одной короткой фразой до 90 символов, в характере: страшно, но весело.",
		spokenName(p.name)), append([]string(nil), a.history...))
	return true
}

// startHover flies beside a creative flyer with creative flight (fork's hoverFollow).
func (a *Agent) startHover(ctx context.Context, nick string) bool {
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
		a.log.Warn("hover stop", "err", err)
	}
	text, err := a.mc.Call(ctx, "hoverFollow", map[string]any{"nick": nick})
	if err != nil {
		a.log.Warn("hoverFollow", "err", err)
		return false
	}
	var st struct {
		Phase  string `json:"phase"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(text), &st) != nil || st.Phase != "flying" {
		a.log.Info("hover: not started", "nick", nick, "reason", st.Reason)
		return false
	}
	a.hovering = true
	a.followAfter = nick
	a.log.Info("hover: flying beside", "leader", nick)
	return true
}

// startHoverTo creative-flies to x, z with no elytra: straight up out of a pit, across, down onto
// the top surface there. Creative with the fork only.
func (a *Agent) startHoverTo(ctx context.Context, x, z int) bool {
	if !a.creative || !a.fork {
		return false
	}
	text, err := a.mc.Call(ctx, "hoverTo", map[string]any{"x": x, "z": z})
	if err != nil {
		a.log.Warn("hoverTo", "err", err)
		return false
	}
	var st struct {
		Phase  string `json:"phase"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(text), &st) != nil || st.Phase != "flying" {
		a.log.Info("hover: not started", "x", x, "z", z, "reason", st.Reason)
		return false
	}
	a.hovering = true
	a.log.Info("hover: flying to", "x", x, "z", z)
	return true
}

// checkHover polls a creative flight beside someone or to a point; true while it goes on.
func (a *Agent) checkHover(ctx context.Context) bool {
	if !a.hovering {
		return false
	}
	text, err := a.mc.Call(ctx, "hoverStatus", nil)
	if err != nil {
		a.hovering = false
		return false
	}
	var st struct {
		Phase  string  `json:"phase"`
		Reason string  `json:"reason"`
		Gap    float64 `json:"gap"`
		Leader string  `json:"leader"`
	}
	if json.Unmarshal([]byte(text), &st) != nil {
		return true
	}
	if st.Phase == "flying" || st.Phase == "landing" {
		return true
	}
	a.hovering = false
	a.log.Info("hover: "+st.Phase, "reason", st.Reason)
	if st.Phase == "done" && st.Leader != "" {
		a.mood("happy", 6)
		a.celebrate(ctx, "flew together")
	}
	return false
}

// checkFlight polls an ongoing flight; true while the bot is still in the air or taking off.
func (a *Agent) checkFlight(ctx context.Context) bool {
	if !a.flying {
		return false
	}
	text, err := a.mc.Call(ctx, "flyStatus", nil)
	if err != nil {
		a.flying = false
		return false
	}
	var st struct {
		Phase  string `json:"phase"`
		Reason string `json:"reason"`
		Rocket int    `json:"rocketsUsed"`
		Sky    string `json:"openSky"`
	}
	_ = json.Unmarshal([]byte(text), &st)
	var tel struct {
		Pos      string  `json:"pos"`
		Mode     string  `json:"mode"`
		Speed    float64 `json:"speed"`
		Pitch    float64 `json:"pitch"`
		Climb    float64 `json:"climbAngle"`
		Distance float64 `json:"distance"`
	}
	if json.Unmarshal([]byte(text), &tel) == nil && tel.Mode != "" && st.Phase == "flying" {
		// Telemetry from the fork's pilot, for tuning the flight from the log.
		a.log.Info("flight: status", "mode", tel.Mode, "speed", tel.Speed, "pitch", tel.Pitch,
			"climb", tel.Climb, "dist", tel.Distance)
	}
	if p, err := explore.ParsePos(tel.Pos); err == nil {
		x, z := explore.Block(p.X), explore.Block(p.Z)
		if n := len(a.track); n == 0 || math.Hypot(float64(x-a.track[n-1][0]), float64(z-a.track[n-1][1])) > 4 {
			a.track = append(a.track, [2]int{x, z})
		}
	}
	switch st.Phase {
	case "done", "failed", "idle":
		a.flying = false
		a.landedAt = time.Now()
		a.applyFallSaves(ctx)
		a.log.Info("flight: "+st.Phase, "reason", st.Reason, "rockets", st.Rocket,
			"seconds", int(time.Since(a.flightStarted).Seconds()))
		if a.places != nil {
			a.places.AddTrack(explore.Track{Style: a.trackStyle, At: a.flightStarted, Points: a.track})
			a.savePlaces()
		}
		a.track = nil
		if st.Phase == "done" {
			a.mood("happy", 6) // down safe
			// Now and then one pretty rocket on landing, for the joy of it.
			a.celebrateAt = time.Now().Add(3 * time.Second) // once the feet are on the ground
		} else {
			a.mood("neutral", 1)
		}
		if st.Phase != "failed" {
			a.roofRetries = 0
		}
		if st.Phase == "failed" {
			a.flightFailedAt = time.Now()
			// Creative, a flight to a point: creative flight is the sure way out from under a roof.
			// The open sky the mod finds may be a column far below an edge (it accepts those in
			// creative), which the pathfinder will not walk down to -- two retries lost.
			if a.creative && a.flyPoint && st.Reason == "under a roof" && a.startHoverTo(ctx, a.flyX, a.flyZ) {
				a.flyOut = false
				a.log.Info("flight: under a roof, hovering there", "x", a.flyX, "z", a.flyZ)
				return false
			}
			// Under trees or a roof with open sky close by: step out there and take off again.
			if sx, sz, ok := parseBlockPos(st.Sky); ok && st.Reason == "under a roof" && a.roofRetries < 2 {
				a.roofRetries++
				cmd := fmt.Sprintf("@goto %d %d", sx, sz)
				if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": cmd}); err == nil {
					a.retryFlight = &[2]int{a.flyX, a.flyZ}
					a.retryFrom = [2]int{sx, sz}
					a.retrySince = time.Now()
					a.log.Info("flight: stepping out from under the roof", "cmd", cmd)
					return false
				}
			}
			// Stuck and the elytra cannot go up from here (a pit, a roof): creative flight needs no
			// take-off run and no open sky -- straight up and out, where it can.
			if a.flyOut && st.Reason != "in water" && a.startHoverTo(ctx, a.flyX, a.flyZ) {
				a.flyOut = false
				a.log.Info("stuck: elytra could not go up, hovering out", "reason", st.Reason)
				return false
			}
			// Any other flight to a point from under a roof with no open sky within reach (letters
			// hanging under a platform: every column round them drops to the ground far below, and
			// on foot there is no way down): creative flight goes out sideways and on to the point.
			if a.flyPoint && st.Reason == "under a roof" && a.startHoverTo(ctx, a.flyX, a.flyZ) {
				a.log.Info("flight: no open sky near, hovering there", "x", a.flyX, "z", a.flyZ)
				return false
			}
			// Under a roof, boxed in, in water: go there on foot instead (walks out of a bunker).
			if a.followAfter == "" && (st.Reason == "under a roof" || st.Reason == "blocked" || st.Reason == "in water") {
				cmd := fmt.Sprintf("@goto %d %d", a.flyX, a.flyZ)
				if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": cmd}); err != nil {
					a.log.Warn("walk after failed flight", "err", err)
				} else {
					a.log.Info("flight failed, walking", "reason", st.Reason, "cmd", cmd)
				}
			}
		}
		if st.Phase == "failed" && (st.Reason == "no elytra" || st.Reason == "no fireworks") && !a.creative {
			a.noFlight = true // survival without the kit: walk from now on
		}
		a.flyOut = false
		return false
	}
	return true
}

// supplyCreative hands rockets to a player in creative: take them from the creative tab, then
// let the mod carry them over.
func (a *Agent) supplyCreative(ctx context.Context, nick string) string {
	if !a.creativeGive(ctx, "firework_rocket", 16) {
		return ""
	}
	return fmt.Sprintf("@give %s firework_rocket 16", nick)
}

// signsNote lists signs around the bot for a "where is ..." question, and remembers them as
// places: a sign 50 blocks away answers "where is the pumpkin farm" better than a guess.
func (a *Agent) signsNote(ctx context.Context) string {
	if !a.fork || !a.havePos {
		return ""
	}
	var b strings.Builder
	added := false
	for i, s := range a.signsNear(ctx, 64) {
		text := signText(s)
		x, z, ok := parseBlockPos(s.Pos)
		if text == "" || !ok || i >= 8 {
			continue
		}
		if r := []rune(text); len(r) > 60 {
			text = string(r[:60])
		}
		fmt.Fprintf(&b, "- табличка «%s»: %d блоков на %s\n", text, int(s.Distance),
			explore.Direction(a.pos, float64(x), float64(z)))
		if a.places != nil && a.places.Add(explore.Place{Desc: "табличка «" + text + "»", X: x, Z: z, Seen: time.Now()}) {
			added = true
		}
	}
	if added {
		a.savePlaces()
	}
	return b.String()
}

// placeLabel prefers a sign's text over the block-based description.
func placeLabel(signs []sign, f explore.Finding) (string, bool) {
	desc, ok := f.Landmark()
	if name := signName(signs); name != "" {
		if ok {
			return name + " — " + desc, true
		}
		return name, true
	}
	return desc, ok
}

// playerPos finds a player the client can see (render distance) by nick or by the name
// players use for them ("нетян" → NetTyan). ok=false when they are out of sight.
func (a *Agent) playerPos(ctx context.Context, name string) (nick string, x, z, dist float64, ok bool) {
	text, err := a.mc.Call(ctx, "getGameState", nil)
	if err != nil {
		return "", 0, 0, 0, false
	}
	var st struct {
		Players []struct {
			Name     string `json:"name"`
			Pos      string `json:"pos"`
			Distance string `json:"distance"`
		} `json:"players"`
	}
	if json.Unmarshal([]byte(text), &st) != nil {
		return "", 0, 0, 0, false
	}
	want := strings.ToLower(name)
	for _, p := range st.Players {
		if strings.ToLower(p.Name) != want && strings.ToLower(spokenName(p.Name)) != want &&
			!strings.HasPrefix(strings.ToLower(p.Name), want) {
			continue
		}
		pos, err := explore.ParsePos(p.Pos)
		if err != nil {
			return "", 0, 0, 0, false
		}
		var d float64
		fmt.Sscanf(p.Distance, "%g", &d)
		return p.Name, pos.X, pos.Z, d, true
	}
	return "", 0, 0, 0, false
}

// flyToPlayer takes off towards a player and follows them on foot after landing.
func (a *Agent) flyToPlayer(ctx context.Context, name string) bool {
	nick, x, z, _, ok := a.playerPos(ctx, name)
	if !ok {
		a.log.Info("fly to player: not in sight", "name", name)
		return false
	}
	if !a.startFlight(ctx, int(x), int(z)) {
		return false
	}
	a.followAfter = nick
	a.log.Info("flight: to player", "nick", nick, "x", int(x), "z", int(z))
	return true
}

// checkWater gets the bot out of a lake: the mod's pathfinder is poor in water and elytra do not
// take off from it, so a bot that wandered in stayed for minutes. After a few seconds in water
// it swims to the nearest dry shore (fork's swimToShore) and the rest of roam waits.
func (a *Agent) checkWater(ctx context.Context) bool {
	if !a.fork || a.noSwim || a.flying {
		return false
	}
	if a.swimming {
		text, err := a.mc.Call(ctx, "swimStatus", nil)
		var st struct {
			Phase  string `json:"phase"`
			Reason string `json:"reason"`
		}
		if err == nil && json.Unmarshal([]byte(text), &st) == nil && st.Phase == "swimming" {
			return true
		}
		a.swimming = false
		a.wetSince = time.Time{}
		a.log.Info("water: "+st.Phase, "reason", st.Reason)
		if st.Phase == "failed" {
			a.swimPauseUntil = time.Now().Add(30 * time.Second)
		}
		return false
	}
	text, err := a.mc.Call(ctx, "getGameState", nil)
	if err != nil {
		return false
	}
	var gs struct {
		Self struct {
			InWater bool `json:"inWater"`
		} `json:"self"`
	}
	if json.Unmarshal([]byte(text), &gs) != nil || !gs.Self.InWater {
		a.wetSince = time.Time{}
		return false
	}
	if a.wetSince.IsZero() {
		a.wetSince = time.Now()
		return false
	}
	if time.Since(a.wetSince) < 4*time.Second || time.Now().Before(a.swimPauseUntil) {
		return false
	}
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
		a.log.Warn("water stop", "err", err)
	}
	a.dropScoutTarget(a.activity)                                           // a scout that led into water: not that chunk again
	out, err := a.mc.Call(ctx, "swimToShore", map[string]any{"radius": 48}) // the most the fork looks
	if err != nil {
		if unknownTool(err) {
			a.noSwim = true // an older fork jar
		} else {
			a.log.Warn("swim to shore", "err", err)
		}
		return false
	}
	// The walk that led into the lake is dropped; companions and activities pick up after.
	a.trip, a.outing, a.activity, a.following, a.leading = nil, nil, nil, "", false
	a.swimming = true
	a.mood("scared", 12)
	a.log.Info("water: swimming to shore", "result", out)
	return true
}

// salute launches colored rockets from the ground (fork, creative), n of them in a fan ahead.
// The bot stops walking first, so the pathfinder does not turn it away.
func (a *Agent) salute(ctx context.Context, n int) bool {
	if !a.fork || !a.creative || a.flying {
		a.log.Info("salute skipped", "fork", a.fork, "creative", a.creative, "flying", a.flying)
		return false
	}
	a.trip, a.activity, a.outing = nil, nil, nil
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
		a.log.Warn("salute stop", "err", err)
	}
	text, err := a.mc.Call(ctx, "salute", map[string]any{"count": n})
	if err != nil {
		a.log.Warn("salute", "err", err)
		return false
	}
	a.log.Info("salute", "count", n, "result", text)
	var st struct {
		Phase string `json:"phase"`
	}
	if json.Unmarshal([]byte(text), &st) == nil && st.Phase == "failed" {
		return false // not on the ground, no rockets: no show, nothing to stand and watch
	}
	a.lastRocketShow = time.Now()
	a.mood("happy", 15)
	a.lingerFor(15 * time.Second) // stay and watch it
	return true
}

// seenPlayer is a player in sight (loaded by the client), not the bot and not another bot.
type seenPlayer struct {
	name       string
	x, z, dist float64
}

func (a *Agent) visiblePlayers(ctx context.Context) []seenPlayer {
	text, err := a.mc.Call(ctx, "getGameState", nil)
	if err != nil {
		return nil
	}
	var st struct {
		Players []struct {
			Name     string `json:"name"`
			Pos      string `json:"pos"`
			Distance any    `json:"distance"`
		} `json:"players"`
	}
	if json.Unmarshal([]byte(text), &st) != nil {
		return nil
	}
	var out []seenPlayer
	for _, p := range st.Players {
		if strings.EqualFold(p.Name, a.cfg.BotNick) ||
			slices.ContainsFunc(a.cfg.OtherBots, func(b string) bool { return strings.EqualFold(b, p.Name) }) {
			continue
		}
		pos, err := explore.ParsePos(p.Pos)
		if err != nil {
			continue
		}
		var d float64
		fmt.Sscanf(fmt.Sprint(p.Distance), "%g", &d)
		out = append(out, seenPlayer{name: p.Name, x: pos.X, z: pos.Z, dist: d})
	}
	return out
}
