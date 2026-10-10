package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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

// protectBuilds asks the pathfinder to mine only natural terrain and place nothing. With the
// upstream mod it falls back to a no-break zone around home.
func (a *Agent) protectBuilds(ctx context.Context) {
	out, err := a.mc.Call(ctx, "setBuildPolicy", map[string]any{"breakOnlyNatural": true, "allowPlace": false})
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
	if creative != a.creative || !a.modeKnown {
		a.log.Info("game mode", "mode", st.Self.GameMode)
	}
	a.creative, a.modeKnown = creative, true

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
	return a.startFlightLow(ctx, x, z, false)
}

// startFlightLow takes off for x, z; low is a low-level flight (бреющий) a few blocks over the
// ground. An older fork jar ignores it and flies at the usual height.
func (a *Agent) startFlightLow(ctx context.Context, x, z int, low bool) bool {
	y := 70
	if a.places != nil && a.places.Home != nil {
		y = int(a.places.Home.Y)
	}
	args := map[string]any{"x": x, "y": y, "z": z}
	if low {
		args["low"] = true
	}
	if !a.takeOff(ctx, "flyTo", args) {
		return false
	}
	a.flyX, a.flyZ = x, z
	a.log.Info("flight: taking off", "x", x, "z", z, "low", low)
	return true
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
	}
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
		a.log.Warn("flight stop walk", "err", err)
	}
	a.following = "" // the @stop above ends any follow; callers set followAfter to resume it
	a.flying = true  // fall saves off before take-off: a glide must not trigger pearl throws
	a.applyFallSaves(ctx)
	if _, err := a.mc.Call(ctx, tool, args); err != nil {
		if unknownTool(err) {
			a.noFlight = true // upstream mod: never try again this run
		} else {
			a.log.Warn("fly", "err", err)
		}
		a.flying = false
		a.applyFallSaves(ctx)
		return false
	}
	a.flying, a.flightStarted = true, time.Now()
	return true
}

// leaderFlying reports whether the player the bot follows is flying an elytra right now.
func (a *Agent) leaderFlying(ctx context.Context, nick string) bool {
	text, err := a.mc.Call(ctx, "getGameState", nil)
	if err != nil {
		return false
	}
	var st struct {
		Players []struct {
			Name    string `json:"name"`
			Gliding bool   `json:"gliding"`
		} `json:"players"`
	}
	if json.Unmarshal([]byte(text), &st) != nil {
		return false
	}
	for _, p := range st.Players {
		if strings.EqualFold(p.Name, nick) {
			return p.Gliding
		}
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
	switch st.Phase {
	case "done", "failed", "idle":
		a.flying = false
		a.applyFallSaves(ctx)
		a.log.Info("flight: "+st.Phase, "reason", st.Reason, "rockets", st.Rocket,
			"seconds", int(time.Since(a.flightStarted).Seconds()))
		if st.Phase != "failed" {
			a.roofRetries = 0
		}
		if st.Phase == "failed" {
			a.flightFailedAt = time.Now()
			// Under trees or a roof with open sky close by: step out there and take off again.
			if sx, sz, ok := parseBlockPos(st.Sky); ok && st.Reason == "under a roof" && a.roofRetries < 2 {
				a.roofRetries++
				cmd := fmt.Sprintf("@goto %d %d", sx, sz)
				if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": cmd}); err == nil {
					a.retryFlight = &[2]int{a.flyX, a.flyZ}
					a.retryFrom = [2]int{sx, sz}
					a.log.Info("flight: stepping out from under the roof", "cmd", cmd)
					return false
				}
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
	out, err := a.mc.Call(ctx, "swimToShore", map[string]any{"radius": 32})
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
	a.log.Info("water: swimming to shore", "result", out)
	return true
}
