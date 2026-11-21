package agent

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/alteralto/persona-agent/internal/chatlog"
	"github.com/alteralto/persona-agent/internal/explore"
)

const (
	nearbyChatRadius  = 12 // blocks, including height
	nearbyVoiceRadius = 24 // blocks: a question said aloud this close is for the bot too
	nearbyReplyEvery  = 8 * time.Second
	nearbyPerMinute   = 3  // unnamed answers to one player a minute
	faceTalkRadius    = 6  // blocks: a player this close and looking at the bot talks to it
	faceTalkAngle     = 25 // degrees off the line to the bot that still count as looking at it
)

func (a *Agent) nearbyConversation(ctx context.Context, msg chatlog.Message, source string, now time.Time) bool {
	if (source != "voice" && !msg.NearbyChat) || now.Sub(a.lastChime) < nearbyReplyEvery || a.judging() {
		return false
	}
	// One player gets a few unnamed answers a minute, not a reply to every line: a troll kept him
	// answering "Ты жопа" and "А то с лопом" back to back (21.11). Called by name still counts.
	key := strings.ToLower(msg.Nick)
	if a.nearbyAnswers == nil {
		a.nearbyAnswers = map[string][]time.Time{}
	}
	recent := a.nearbyAnswers[key][:0]
	for _, t := range a.nearbyAnswers[key] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	a.nearbyAnswers[key] = recent
	if len(recent) >= nearbyPerMinute {
		return false
	}

	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!") {
		return false
	}
	tokens := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	if len(tokens) == 0 {
		return false
	}
	// Speech transcripts often omit the question mark.
	opening := slices.Contains(toEveryone, tokens[0]) || slices.Contains([]string{
		"здравствуй", "здрасте", "приветик", "даров", "здарова", "салют", "ку", "хай", "hello", "hi",
		"ты", "вы", "как", "где", "что", "куда", "почему", "зачем", "чей", "сколько", "можешь", "слышишь", "подскажи",
		"ало", "алло", "алё", "слушай", "бро", "чел", "йо", "hey", "yo",
	}, tokens[0]) || strings.Contains(text, "?")
	followup := strings.EqualFold(msg.Nick, a.partner) && now.Sub(a.partnerAt) < 45*time.Second
	if namesSomeone(text) && tokens[0] != "подскажи" && tokens[0] != "слушай" {
		return false
	}

	if now.Sub(a.chatPlayersAt) >= 2*time.Second || a.chatPlayersAt.IsZero() {
		// One lookup per burst, even when many people ask questions at once.
		a.chatPlayers, a.chatFacing, a.chatPlayersAt = nil, nil, now
		raw, err := a.mc.Call(ctx, "getGameState", nil)
		var state struct {
			Players []struct {
				Name     string
				Pos      string
				Distance json.Number
				Yaw      *float64 // not sent by the mod yet (21.11)
			}
		}
		if err != nil || json.Unmarshal([]byte(raw), &state) != nil {
			return false
		}
		a.chatPlayers = make(map[string]bool, len(state.Players))
		a.chatFacing = make(map[string]bool)
		a.chatDist = make(map[string]float64, len(state.Players))
		for _, p := range state.Players {
			d, err := p.Distance.Float64()
			a.chatPlayers[strings.ToLower(p.Name)] = err == nil && d >= 0 && d <= nearbyChatRadius
			if err == nil && d >= 0 {
				a.chatDist[strings.ToLower(p.Name)] = d
			}
			if err != nil || d > faceTalkRadius || p.Yaw == nil {
				continue
			}
			// Right next to the bot and looking at it: what they say is said to the bot, opener
			// or not. Needs the players' yaw from the mod; without it nobody counts (a player
			// alone beside the bot typing to a friend elsewhere is not talking to it).
			pp, perr := explore.ParsePos(p.Pos)
			if perr != nil || !a.havePos {
				continue
			}
			toBot := math.Atan2(-(a.pos.X-pp.X), a.pos.Z-pp.Z) * 180 / math.Pi // Minecraft yaw
			off := math.Mod(math.Abs(*p.Yaw-toBot), 360)
			a.chatFacing[strings.ToLower(p.Name)] = math.Min(off, 360-off) <= faceTalkAngle
		}
	}
	speaker := strings.ToLower(msg.Nick)
	// Voice carries further than the local chat: "ты кто" said aloud from 15-20 blocks went
	// unanswered on SkyBars (21.11).
	d, seen := a.chatDist[speaker]
	if !seen || d > nearbyChatRadius && (source != "voice" || d > nearbyVoiceRadius) {
		return false
	}
	if !opening && !followup && !a.chatFacing[speaker] {
		return false
	}
	for _, token := range tokens[:min(3, len(tokens))] {
		if _, visible := a.chatPlayers[token]; visible && token != speaker {
			return false
		}
	}
	a.nearbyAnswers[key] = append(a.nearbyAnswers[key], now)
	return true
}
