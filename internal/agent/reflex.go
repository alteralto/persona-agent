package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The mod has reflexes of its own: hit, it hits back and chases ("[Tungsten] Punking player:
// pro228", "Following: pro228"); in danger it runs ("goal=fleeLive(1 danger(s), d=13.0)").
// On SkyBars (21.11) the agent knew nothing of either: it kept sending its own walks over them,
// each one a stop and a restart -- part of the jerky walking -- and said nothing to the one who hit
// it. Now a reflex holds the agent's walks for a while, and the bot has a word for the attacker.

const (
	reflexHold     = 8 * time.Second  // after a reflex line: no walks of the agent's own
	fightTalkEvery = time.Minute      // one word per attacker this often
	fightTalkAny   = 15 * time.Second // and to anyone at all
)

// noteReflex reads one chat line for the mod's reflexes. true: it was one, nothing more to do.
func (a *Agent) noteReflex(ctx context.Context, line string) bool {
	plain := stripFormatting(line)
	switch {
	case strings.Contains(plain, "Punking player:"):
		name := strings.TrimSpace(plain[strings.Index(plain, "Punking player:")+len("Punking player:"):])
		if f := strings.Fields(name); len(f) > 0 {
			name = f[0]
		}
		a.reflexUntil = time.Now().Add(reflexHold)
		a.stride = nil
		a.answerAttacker(name)
		return true
	case strings.Contains(plain, "goal=fleeLive(") && !strings.Contains(plain, "fleeLive(0 danger"),
		strings.Contains(plain, "Walker: danger"):
		a.reflexUntil = time.Now().Add(reflexHold)
		a.stride = nil
		if !a.inReflex {
			a.log.Info("mod reflex: running from danger; own walks on hold")
		}
		a.inReflex = true
		return true
	case strings.Contains(plain, "Following:") || strings.Contains(plain, "Follow stopped"):
		return strings.Contains(plain, "[Tungsten]")
	}
	return false
}

// reflexing: the mod is fighting or fleeing; the agent sends no walks of its own meanwhile.
func (a *Agent) reflexing() bool {
	if time.Now().Before(a.reflexUntil) {
		return true
	}
	if a.inReflex {
		a.inReflex = false
		a.log.Info("mod reflex over: own walks again")
	}
	return false
}

func (a *Agent) answerAttacker(name string) {
	if !a.inReflex {
		a.log.Info("mod reflex: hit back", "player", name)
	}
	a.inReflex = true
	if name == "" || strings.EqualFold(name, a.cfg.BotNick) {
		return
	}
	if a.foughtWith == nil {
		a.foughtWith = map[string]time.Time{}
	}
	key := strings.ToLower(name)
	if time.Since(a.foughtWith[key]) < fightTalkEvery || time.Since(a.lastFightTalk) < fightTalkAny {
		return
	}
	a.foughtWith[key], a.lastFightTalk = time.Now(), time.Now()
	a.enqueue(replyJob{
		ambient: true,
		nick:    name,
		prompt: fmt.Sprintf("Игрок %s только что тебя ударил, и ты дал сдачи. Скажи ему одну короткую фразу, "+
			"как игрок в чате сервера: удивись или подколи, без угроз и без мата, до 40 символов.", name),
		history: slices.Clone(a.history),
		at:      time.Now(),
	})
}

// stripFormatting drops Minecraft's § colour codes.
func stripFormatting(s string) string {
	if !strings.ContainsRune(s, '§') {
		return s
	}
	var b strings.Builder
	skip := false
	for _, r := range s {
		if skip {
			skip = false
			continue
		}
		if r == '§' {
			skip = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
