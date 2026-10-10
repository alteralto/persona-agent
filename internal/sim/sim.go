// Package sim stands in for the game, Laya and GigaChat, so the agent's behaviour can be run
// and tested without Minecraft. The world is flat and tiny: the bot walks or flies in straight
// lines, players stand where they are put, chat is a list of lines. It answers the same MCP
// tools the UnionClef fork does, with the same JSON shapes.
package sim

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Options struct {
	BotNick   string
	Mode      string  // "creative" or "survival"
	Fork      bool    // false: the fork's tools answer "Unknown tool", like the upstream mod
	WalkSpeed float64 // blocks per second
	FlySpeed  float64
	FreeSlots int // empty inventory slots at the start
	Elytra    bool
	// GamemodeWorks: "/gamemode creative" in chat switches the bot to creative (an operator).
	GamemodeWorks bool
}

// Event is one thing the agent did to the world, for timelines and test assertions.
type Event struct {
	At   time.Duration // since the world was created
	Kind string        // "chat", "say", "cmd", "tool", "fly", "land"
	Text string
}

type Sign struct {
	X, Y, Z int
	Text    string
}

type building struct {
	x, z, r int
	block   string
}

type World struct {
	mu    sync.Mutex
	opt   Options
	start time.Time
	last  time.Time
	down  bool

	x, y, z   float64
	goal      *[2]float64
	follow    string
	players   map[string]*[2]float64
	pvel      map[string][2]float64 // blocks per second while a player flies
	flyLeader string
	chat      []string
	events    []Event
	signs     []Sign
	buildings []building
	lakes     [][4]float64 // x0, z0, x1, z1
	swimming  bool
	swimTo    [2]float64
	free      int
	rockets   int

	flyPhase  string // "idle", "takeoff", "flying", "done", "failed"
	flyTarget [2]float64
	flyReason string
	manualAt  time.Time // the player last steered the bot by hand
	flyAt     time.Time
	flown     int
}

func New(o Options) *World {
	if o.BotNick == "" {
		o.BotNick = "Pyhtun"
	}
	if o.Mode == "" {
		o.Mode = "creative"
	}
	if o.WalkSpeed == 0 {
		o.WalkSpeed = 40
	}
	if o.FlySpeed == 0 {
		o.FlySpeed = 200
	}
	now := time.Now()
	return &World{opt: o, start: now, last: now, y: 70, players: map[string]*[2]float64{}, pvel: map[string][2]float64{},
		free: o.FreeSlots, flyPhase: "idle"}
}

// --- scripting -------------------------------------------------------------------------------

func (w *World) AddPlayer(name string, x, z float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.players[name] = &[2]float64{x, z}
}

// FlyPlayer makes a player fly an elytra at (vx, vz) blocks per second.
func (w *World) FlyPlayer(name string, vx, vz float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stepLocked()
	w.pvel[name] = [2]float64{vx, vz}
}

// LandPlayer ends a player's flight where they are.
func (w *World) LandPlayer(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stepLocked()
	delete(w.pvel, name)
}

// Say puts a player's chat line into the log the agent reads.
func (w *World) Say(nick, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	line := "<" + nick + "> " + text
	w.chat = append(w.chat, line)
	w.logLocked("chat", line)
}

func (w *World) AddSign(x, y, z int, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.signs = append(w.signs, Sign{x, y, z, text})
}

// AddBuilding puts man-made blocks around (x,z): the bot's survey will call it a landmark.
func (w *World) AddBuilding(x, z, r int, block string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buildings = append(w.buildings, building{x, z, r, block})
}

// AddLake puts water over the rectangle: the bot there is "in water" and walks barely move it.
func (w *World) AddLake(x0, z0, x1, z1 float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lakes = append(w.lakes, [4]float64{min(x0, x1), min(z0, z1), max(x0, x1), max(z0, z1)})
}

// PutBot moves the bot, e.g. into a lake.
func (w *World) PutBot(x, z float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.x, w.z = x, z
}

func (w *World) inWaterLocked(x, z float64) bool {
	for _, l := range w.lakes {
		if x >= l[0] && x <= l[2] && z >= l[1] && z <= l[3] {
			return true
		}
	}
	return false
}

// InWater reports whether the bot is in a lake.
func (w *World) InWater() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stepLocked()
	return w.inWaterLocked(w.x, w.z)
}

// SteerByHand is the player at the keyboard moving the mouse: a flight lets go, as in the mod.
func (w *World) SteerByHand() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stepLocked()
	w.manualAt = time.Now()
	if w.flyPhase == "takeoff" || w.flyPhase == "flying" {
		w.flyPhase, w.flyReason, w.flyLeader = "failed", "by hand", ""
	}
	w.goal, w.follow = nil, "" // the hand wins over the pathfinder
}

// SetDown makes every tool call fail, as when the game is closed.
func (w *World) SetDown(down bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.down = down
}

func (w *World) SetMode(mode string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.opt.Mode = mode
}

func (w *World) SetFreeSlots(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.free = n
}

func (w *World) Bot() (x, z float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stepLocked()
	return w.x, w.z
}

// DistanceTo is how far the bot is from a player.
func (w *World) DistanceTo(name string) float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stepLocked()
	p := w.players[name]
	if p == nil {
		return math.Inf(1)
	}
	return math.Hypot(p[0]-w.x, p[1]-w.z)
}

func (w *World) Events() []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Event(nil), w.events...)
}

// Has reports whether an event of kind contains text.
func (w *World) Has(kind, text string) bool {
	for _, e := range w.Events() {
		if e.Kind == kind && strings.Contains(e.Text, text) {
			return true
		}
	}
	return false
}

// Count is the number of events of kind that contain text.
func (w *World) Count(kind, text string) int {
	n := 0
	for _, e := range w.Events() {
		if e.Kind == kind && strings.Contains(e.Text, text) {
			n++
		}
	}
	return n
}

func (w *World) logLocked(kind, text string) {
	w.events = append(w.events, Event{At: time.Since(w.start), Kind: kind, Text: text})
}

// --- physics ---------------------------------------------------------------------------------

func (w *World) stepLocked() {
	now := time.Now()
	dt := now.Sub(w.last).Seconds()
	w.last = now
	for name, v := range w.pvel {
		if p := w.players[name]; p != nil {
			p[0] += v[0] * dt
			p[1] += v[1] * dt
		}
	}
	switch w.flyPhase {
	case "takeoff":
		if now.Sub(w.flyAt) > 200*time.Millisecond {
			w.flyPhase = "flying"
			w.rockets++
		}
		return
	case "flying":
		if w.flyLeader != "" { // wingman: keep close while they fly, land beside them after
			p := w.players[w.flyLeader]
			if p == nil {
				w.flyPhase, w.flyReason, w.flyLeader = "done", "lost leader", ""
				return
			}
			_, gliding := w.pvel[w.flyLeader]
			w.moveLocked(*p, w.opt.FlySpeed*dt, 5)
			if !gliding {
				w.flyPhase, w.flyReason, w.flyLeader = "done", "arrived", ""
				w.logLocked("land", fmt.Sprintf("%d %d", int(w.x), int(w.z)))
			}
			return
		}
		if w.moveLocked(w.flyTarget, w.opt.FlySpeed*dt, 3) {
			w.flyPhase, w.flyReason = "done", "arrived"
			w.logLocked("land", fmt.Sprintf("%d %d", int(w.x), int(w.z)))
		}
		return
	}
	if w.swimming {
		if w.moveLocked(w.swimTo, 4*dt, 0.3) {
			w.swimming = false
		}
		return
	}
	// Like the real pathfinder: in water a walk goes nowhere.
	if w.inWaterLocked(w.x, w.z) {
		return
	}
	if w.follow != "" {
		if p := w.players[w.follow]; p != nil {
			w.moveLocked(*p, w.opt.WalkSpeed*dt, 2)
		}
		return
	}
	if w.goal != nil && w.moveLocked(*w.goal, w.opt.WalkSpeed*dt, 0.5) {
		w.goal = nil
	}
}

// moveLocked steps towards target; true once within near.
func (w *World) moveLocked(target [2]float64, step, near float64) bool {
	dx, dz := target[0]-w.x, target[1]-w.z
	d := math.Hypot(dx, dz)
	if d <= near {
		return true
	}
	if step >= d-near {
		k := (d - near) / d
		w.x, w.z = w.x+dx*k, w.z+dz*k
		return true
	}
	w.x, w.z = w.x+dx/d*step, w.z+dz/d*step
	return false
}

// --- MCP -------------------------------------------------------------------------------------

// Handler serves the mod's MCP endpoint (JSON-RPC tools/call).
func (w *World) Handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64 `json:"id"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		if w.down {
			w.mu.Unlock()
			http.Error(rw, "game closed", http.StatusServiceUnavailable)
			return
		}
		w.stepLocked()
		out, isErr := w.callLocked(req.Params.Name, req.Params.Arguments)
		w.mu.Unlock()
		text, ok := out.(string)
		if !ok {
			raw, _ := json.Marshal(out)
			text = string(raw)
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr},
		})
	})
}

var forkTools = map[string]bool{"flyFollow": true, "setBuildPolicy": true, "readSigns": true, "creativeGive": true,
	"tidyInventory": true, "flyTo": true, "flyStatus": true, "flyStop": true, "swimToShore": true, "swimStatus": true}

func (w *World) callLocked(tool string, args map[string]any) (any, bool) {
	if forkTools[tool] && !w.opt.Fork {
		return "Unknown tool: " + tool, true
	}
	if tool != "getRecentChat" && tool != "pathStatus" && tool != "getGameState" && tool != "flyStatus" && tool != "swimStatus" {
		w.logLocked("tool", tool+" "+compact(args))
	}
	switch tool {
	case "getRecentChat":
		n := intArg(args, "n", 40)
		lines := w.chat
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		return append([]string{}, lines...), false
	case "ChatMessage":
		msg, _ := args["msg"].(string)
		if strings.HasPrefix(msg, "/") {
			w.logLocked("cmd", msg)
			if msg == "/gamemode creative" && w.opt.GamemodeWorks {
				w.opt.Mode = "creative"
			}
			return "ok", false
		}
		line := "<" + w.opt.BotNick + "> " + msg
		w.chat = append(w.chat, line)
		w.logLocked("say", msg)
		return "ok", false
	case "ExecuteCommand":
		cmd, _ := args["cmd"].(string)
		w.logLocked("cmd", cmd)
		w.commandLocked(cmd)
		return "ok", false
	case "pathStatus":
		busy := w.goal != nil || w.follow != "" || w.flyPhase == "takeoff" || w.flyPhase == "flying"
		manual := int64(-1)
		if !w.manualAt.IsZero() {
			manual = time.Since(w.manualAt).Milliseconds()
		}
		return map[string]any{"pos": fmt.Sprintf("%.1f,%.1f,%.1f", w.x, w.y, w.z), "busy": busy, "manualMs": manual}, false
	case "getGameState":
		var players []map[string]any
		for name, p := range w.players {
			_, gliding := w.pvel[name]
			players = append(players, map[string]any{"name": name, "pos": fmt.Sprintf("%.1f,70.0,%.1f", p[0], p[1]), "gliding": gliding,
				"distance": fmt.Sprintf("%.1f", math.Hypot(p[0]-w.x, p[1]-w.z))})
		}
		return map[string]any{"self": map[string]any{"gameMode": w.opt.Mode, "flying": w.flyPhase == "flying",
			"inWater": w.inWaterLocked(w.x, w.z),
			"pos":     fmt.Sprintf("%.1f,%.1f,%.1f", w.x, w.y, w.z)}, "players": players}, false
	case "setBuildPolicy":
		return map[string]any{"ok": true, "breakOnlyNatural": args["breakOnlyNatural"], "allowPlace": args["allowPlace"]}, false
	case "clearProtectedAreas", "markProtectedArea":
		return map[string]any{"ok": true}, false
	case "readSigns":
		r := float64(intArg(args, "radius", 16))
		var out []map[string]any
		for _, s := range w.signs {
			d := math.Hypot(float64(s.X)-w.x, float64(s.Z)-w.z)
			if d <= r {
				out = append(out, map[string]any{"pos": fmt.Sprintf("%d,%d,%d", s.X, s.Y, s.Z),
					"distance": math.Round(d*10) / 10, "front": s.Text, "back": ""})
			}
		}
		sortByDistance(out)
		if out == nil {
			return []any{}, false
		}
		return out, false
	case "creativeGive":
		item, _ := args["item"].(string)
		switch {
		case w.opt.Mode != "creative":
			return map[string]any{"ok": false, "reason": "not creative"}, false
		case w.free == 0:
			return map[string]any{"ok": false, "given": 0, "reason": "inventory full"}, false
		}
		w.free--
		if item == "elytra" {
			w.opt.Elytra = true
		}
		return map[string]any{"ok": true, "given": intArg(args, "count", 1)}, false
	case "tidyInventory":
		keep := intArg(args, "keepFree", 4)
		freed := 0
		if w.free < keep && w.opt.Mode == "creative" {
			freed, w.free = keep-w.free, keep
		}
		return map[string]any{"ok": w.free >= keep, "free": w.free, "freed": freed, "mode": w.opt.Mode}, false
	case "swimToShore":
		if !w.inWaterLocked(w.x, w.z) {
			return map[string]any{"phase": "done", "reason": "not in water"}, false
		}
		w.goal, w.follow = nil, ""
		w.swimTo, w.swimming = w.shoreLocked(), true
		w.logLocked("swim", fmt.Sprintf("%d %d", int(w.swimTo[0]), int(w.swimTo[1])))
		return map[string]any{"phase": "swimming"}, false
	case "swimStatus":
		phase := "done"
		if w.swimming {
			phase = "swimming"
		}
		return map[string]any{"phase": phase, "reason": "ashore", "inWater": w.inWaterLocked(w.x, w.z)}, false
	case "flyTo":
		if w.inWaterLocked(w.x, w.z) {
			w.flyPhase, w.flyReason = "failed", "in water"
			return map[string]any{"phase": "failed", "reason": "in water"}, false
		}
		if !w.opt.Elytra {
			w.flyPhase, w.flyReason = "failed", "no elytra"
			return map[string]any{"phase": "failed", "reason": "no elytra"}, false
		}
		w.goal, w.follow = nil, ""
		w.flyTarget = [2]float64{float64(intArg(args, "x", 0)), float64(intArg(args, "z", 0))}
		w.flyPhase, w.flyReason, w.flyAt = "takeoff", "", time.Now()
		w.flown++
		w.logLocked("fly", fmt.Sprintf("%d %d", int(w.flyTarget[0]), int(w.flyTarget[1])))
		return map[string]any{"phase": "equip"}, false
	case "flyFollow":
		nick, _ := args["nick"].(string)
		if w.players[nick] == nil {
			return map[string]any{"phase": "failed", "reason": "not in sight"}, false
		}
		if !w.opt.Elytra {
			w.flyPhase, w.flyReason = "failed", "no elytra"
			return map[string]any{"phase": "failed", "reason": "no elytra"}, false
		}
		w.goal, w.follow = nil, ""
		w.flyLeader = nick
		w.flyPhase, w.flyReason, w.flyAt = "takeoff", "", time.Now()
		w.flown++
		w.logLocked("fly", "wingman "+nick)
		return map[string]any{"phase": "equip", "leader": nick}, false
	case "flyStatus":
		return map[string]any{"phase": w.flyPhase, "reason": w.flyReason, "rocketsUsed": w.rockets}, false
	case "flyStop":
		w.flyPhase, w.flyReason = "idle", "stopped"
		return map[string]any{"phase": "idle"}, false
	case "getSurfaceMap":
		return w.surfaceLocked(intArg(args, "radius", 6)), false
	}
	return "Unknown tool: " + tool, true
}

func (w *World) commandLocked(cmd string) {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return
	}
	switch f[0] {
	case "@goto":
		nums := f[1:]
		if len(nums) == 3 {
			nums = []string{nums[0], nums[2]}
		}
		if len(nums) == 2 {
			x, err1 := strconv.Atoi(nums[0])
			z, err2 := strconv.Atoi(nums[1])
			if err1 == nil && err2 == nil {
				w.goal, w.follow = &[2]float64{float64(x), float64(z)}, ""
			}
		}
	case "@follow":
		if len(f) > 1 {
			w.follow, w.goal = f[1], nil
		}
	case "@stop":
		w.goal, w.follow = nil, ""
	}
}

// shoreLocked is the nearest point just outside the lake the bot is in.
func (w *World) shoreLocked() [2]float64 {
	for _, l := range w.lakes {
		if !(w.x >= l[0] && w.x <= l[2] && w.z >= l[1] && w.z <= l[3]) {
			continue
		}
		best, bestD := [2]float64{l[0] - 1, w.z}, w.x-l[0]
		for _, c := range []struct {
			p [2]float64
			d float64
		}{{[2]float64{l[2] + 1, w.z}, l[2] - w.x}, {[2]float64{w.x, l[1] - 1}, w.z - l[1]}, {[2]float64{w.x, l[3] + 1}, l[3] - w.z}} {
			if c.d < bestD {
				best, bestD = c.p, c.d
			}
		}
		return best
	}
	return [2]float64{w.x, w.z}
}

func (w *World) surfaceLocked(radius int) []map[string]string {
	var cols []map[string]string
	for dx := -radius; dx <= radius; dx++ {
		for dz := -radius; dz <= radius; dz++ {
			bx, bz := int(w.x)+dx, int(w.z)+dz
			block := "Блок травы"
			for _, b := range w.buildings {
				if abs(bx-b.x) <= b.r && abs(bz-b.z) <= b.r {
					block = b.block
				}
			}
			cols = append(cols, map[string]string{"dx": strconv.Itoa(dx), "dz": strconv.Itoa(dz), "block": block})
		}
	}
	return cols
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

func compact(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	raw, _ := json.Marshal(args)
	return string(raw)
}

func sortByDistance(s []map[string]any) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j]["distance"].(float64) < s[j-1]["distance"].(float64); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
