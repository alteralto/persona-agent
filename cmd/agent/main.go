package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/explore"
	"github.com/alteralto/persona-agent/internal/giga"
	"github.com/alteralto/persona-agent/internal/laya"
	"github.com/alteralto/persona-agent/internal/mcp"
	"github.com/alteralto/persona-agent/internal/twitch"
	"github.com/alteralto/persona-agent/internal/voice"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	loadDotEnv(".env")

	botNick := env("BOT_NICK", "")
	if botNick == "" {
		fatal(log, "BOT_NICK is required (the bot's in-game nickname)")
	}
	creds := env("GIGACHAT_CREDENTIALS", "")
	if creds == "" {
		fatal(log, "GIGACHAT_CREDENTIALS is required")
	}
	token, err := mcp.TokenFromSettings(env("MC_SETTINGS", mcp.DefaultSettings()))
	if err != nil {
		fatal(log, "read MCP token", "err", err)
	}

	cfg := agent.Config{
		BotNick:         botNick,
		Aliases:         splitList(env("BOT_ALIASES", "пыхтун,пыхтуна,пыхтуну,агент")),
		OtherBots:       splitList(env("OTHER_BOTS", "")),
		Creative:        env("SERVER_MODE", "creative") == "creative",
		MinConfidence:   envFloat("LAYA_MIN_PROB", 0.45),
		PollEvery:       500 * time.Millisecond,
		MaxBotReplies:   int(envFloat("MAX_BOT_REPLIES", 0)), // 0 = unlimited
		RequireMention:  env("REQUIRE_MENTION", "true") == "true",
		ReplyEvery:      envDuration("REPLY_EVERY", replyEveryDefault()), // floor; lines also wait for the one before to finish (pacing.go)
		IdleEvery:       envDuration("IDLE_EVERY", 4*time.Minute),
		Intro:           env("INTRO", "true") == "true",
		Explore:         env("EXPLORE", "true") == "true",
		ExploreRadius:   int(envFloat("EXPLORE_RADIUS", 6)),
		ExploreAfter:    envDuration("EXPLORE_AFTER", 45*time.Second),
		PlacesPath:      env("PLACES_PATH", `data\places.json`),
		Companions:      splitList(env("COMPANIONS", "")),
		JournalPath:     env("JOURNAL_PATH", `data\decisions.jsonl`),
		PeopleDir:       env("PEOPLE_DIR", `data\people`),
		PublicServer:    env("PUBLIC_SERVER", "0") == "1",
		PublicCreative:  env("PUBLIC_CREATIVE", "1") == "1",
		AutoCreative:    env("AUTO_CREATIVE", "false") == "true",
		ActivityEvery:   envDuration("ACTIVITY_EVERY", 30*time.Second),
		NoOwnGames:      env("OWN_GAMES", "1") == "0",
		FlightShare:     envFloat("FLIGHT_SHARE", 0),
		Home:            homeFromEnv(env("HOME_XZ", "")),
		CreativeCommand: env("CREATIVE_COMMAND", "/gamemode creative"),
		OthersNames:     splitList(env("OTHERS_NAMES", "")),
		ProtectRadius:   int(envFloat("PROTECT_RADIUS", 256)),
		AllowBreak:      env("ALLOW_BREAK", "false") == "true",
		FlightStyle:     env("FLIGHT_STYLE", ""),
		Solver:          env("FLY_SOLVER", "0") == "1",
		HoverOnly:       env("HOVER_ONLY", "0") == "1",
		MarksDir:        env("MARKS_DIR", filepath.Join(mcp.GameDir(), "xaero", "minimap")),
		XaeroServer:     env("XAERO_SERVER", "Multiplayer_agicraft.ru"),
	}
	llm := giga.New(creds, env("GIGACHAT_SCOPE", "GIGACHAT_API_PERS"), env("GIGACHAT_MODEL", "GigaChat-2"),
		env("GIGACHAT_VERIFY_SSL", "false") == "true")
	llm.PersistDNS(env("DNS_CACHE", `data\dns-cache.json`))
	// MCP_URL pins one mod server; by default the bot's own client is found by its player name
	// among 127.0.0.1:25350..25359 (two game clients on one machine take neighbouring ports).
	mc := mcp.ForPlayer(cfg.BotNick, token, 25350)
	if u := env("MCP_URL", ""); u != "" {
		mc = mcp.New(u, token)
	}
	// The installed fork exposes relative pitch through its local Py4J camera API.
	// Start the adapter only for a steep downward gaze during an autonomous walk.
	if cfg.PublicServer {
		python := filepath.Join(".venv-voice", "Scripts", "pythonw.exe")
		if _, err := os.Stat(python); err == nil {
			cfg.LevelWalkView = func(ctx context.Context) error {
				return exec.CommandContext(ctx, python, "-I", "-B", filepath.Join("scripts", "walk_view.py"), cfg.BotNick).Run()
			}
		}
	}
	a := agent.New(cfg,
		mc,
		laya.New(env("LAYA_URL", "http://127.0.0.1:8090")),
		llm,
		log)

	if m := env("GIGACHAT_SMART_MODEL", "GigaChat-2-Max"); m != "" && m != "none" {
		a.WithSmartModel(llm.WithModel(m)) // shares token, connections and the address cache
	}
	if tuned, err := agent.LoadCriteria(env("LAYA_CRITERIA", `data\laya_criteria.json`)); err != nil {
		log.Warn("laya criteria", "err", err)
	} else if tuned {
		log.Info("laya criteria: tuned set loaded")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if path := os.Getenv("AGENT_CPU_PROFILE"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			log.Warn("CPU profile file", "err", err)
		} else if err := pprof.StartCPUProfile(f); err != nil {
			f.Close()
			log.Warn("CPU profile start", "err", err)
		} else {
			profileCtx, finish := context.WithTimeout(ctx, time.Minute)
			done := make(chan struct{})
			go func() {
				<-profileCtx.Done()
				pprof.StopCPUProfile()
				f.Close()
				close(done)
			}()
			defer func() { finish(); <-done }()
		}
	}
	if env("VOICE", "true") == "true" {
		heard := a.WithVoice(voice.NewSpeaker(env("VOICE_SAY_URL", "http://127.0.0.1:8091/say")))
		go func() {
			if err := voice.Listen(ctx, env("VOICE_LISTEN", "127.0.0.1:8092"), heard, a.Status); err != nil {
				log.Error("voice listener", "err", err)
			}
		}()
	}
	// The stream chat, read-only and anonymous: TWITCH_CHANNEL=name (empty: off).
	if ch := env("TWITCH_CHANNEL", ""); ch != "" {
		go (&twitch.Reader{Channel: ch, Log: log}).Run(ctx, a.WithTwitch())
	}
	log.Info("pyhtun starting", "bot", cfg.BotNick, "creative", cfg.Creative, "public_server", cfg.PublicServer)
	if err := a.Run(ctx); err != nil {
		fatal(log, "agent stopped", "err", err)
	}
}

// homeFromEnv reads "x,z" (HOME_XZ); anything else means no fixed home.
func homeFromEnv(s string) *explore.Pos {
	var x, z float64
	if _, err := fmt.Sscanf(strings.ReplaceAll(s, " ", ""), "%g,%g", &x, &z); err != nil {
		return nil
	}
	return &explore.Pos{X: x, Z: z}
}

func fatal(log *slog.Logger, msg string, args ...any) {
	log.Error(msg, args...)
	os.Exit(1)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if f, err := strconv.ParseFloat(env(key, ""), 64); err == nil {
		return f
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(env(key, "")); err == nil {
		return d
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loadDotEnv sets variables from a KEY=VALUE file without overriding the real environment.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}

// replyEveryDefault: on a public server players wait for answers and the voice paces the lines
// (pacing.go), so the floor is short; on a private text server two bots at 1.5 s would chatter
// at each other, so the old 5 s stays.
func replyEveryDefault() time.Duration {
	if env("PUBLIC_SERVER", "0") == "1" {
		return 1500 * time.Millisecond
	}
	return 5 * time.Second
}
