package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
	}
	if json.Unmarshal([]byte(text), &st) != nil || st.Self.GameMode == "" {
		return
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
	if a.noFlight || !a.fork { // upstream mod: no flyTo, no creativeGive
		return false
	}
	if a.creative && !a.flightKit {
		a.tidyInventory(ctx)
		a.flightKit = a.creativeGive(ctx, "elytra", 1) && a.creativeGive(ctx, "firework_rocket", 64)
	}
	y := 70
	if a.places != nil && a.places.Home != nil {
		y = int(a.places.Home.Y)
	}
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": "@stop"}); err != nil {
		a.log.Warn("flight stop walk", "err", err)
	}
	a.following = "" // the @stop above ends any follow; callers set followAfter to resume it
	a.flying = true  // fall saves off before take-off: a glide must not trigger pearl throws
	a.applyFallSaves(ctx)
	if _, err := a.mc.Call(ctx, "flyTo", map[string]any{"x": x, "y": y, "z": z}); err != nil {
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
	a.log.Info("flight: taking off", "x", x, "z", z)
	return true
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
	}
	_ = json.Unmarshal([]byte(text), &st)
	switch st.Phase {
	case "done", "failed", "idle":
		a.flying = false
		a.applyFallSaves(ctx)
		a.log.Info("flight: "+st.Phase, "reason", st.Reason, "rockets", st.Rocket,
			"seconds", int(time.Since(a.flightStarted).Seconds()))
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
