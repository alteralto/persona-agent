package main

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/giga"
	"github.com/alteralto/persona-agent/internal/laya"
	"github.com/alteralto/persona-agent/internal/mcp"
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
	token, err := mcp.TokenFromSettings(env("MC_SETTINGS",
		`F:\.tlauncher\legacy\Minecraft\game\altoclef\altoclef_settings.json`))
	if err != nil {
		fatal(log, "read MCP token", "err", err)
	}

	cfg := agent.Config{
		BotNick:         botNick,
		Aliases:         splitList(env("BOT_ALIASES", "пыхтун,пыхтуна,пыхтуну")),
		OtherBots:       splitList(env("OTHER_BOTS", "")),
		Creative:        env("SERVER_MODE", "creative") == "creative",
		MinConfidence:   envFloat("LAYA_MIN_PROB", 0.45),
		PollEvery:       500 * time.Millisecond,
		MaxBotReplies:   int(envFloat("MAX_BOT_REPLIES", 0)), // 0 = unlimited
		RequireMention:  env("REQUIRE_MENTION", "true") == "true",
		ReplyEvery:      5 * time.Second,
		IdleEvery:       envDuration("IDLE_EVERY", 4*time.Minute),
		Intro:           env("INTRO", "true") == "true",
		Explore:         env("EXPLORE", "true") == "true",
		ExploreRadius:   int(envFloat("EXPLORE_RADIUS", 6)),
		ExploreAfter:    envDuration("EXPLORE_AFTER", 45*time.Second),
		PlacesPath:      env("PLACES_PATH", `data\places.json`),
		Companions:      splitList(env("COMPANIONS", "")),
		JournalPath:     env("JOURNAL_PATH", `data\decisions.jsonl`),
		PeopleDir:       env("PEOPLE_DIR", `data\people`),
		AutoCreative:    env("AUTO_CREATIVE", "false") == "true",
		CreativeCommand: env("CREATIVE_COMMAND", "/gamemode creative"),
		OthersNames:     splitList(env("OTHERS_NAMES", "")),
		ProtectRadius:   int(envFloat("PROTECT_RADIUS", 256)),
	}
	llm := giga.New(creds, env("GIGACHAT_SCOPE", "GIGACHAT_API_PERS"), env("GIGACHAT_MODEL", "GigaChat-2"),
		env("GIGACHAT_VERIFY_SSL", "false") == "true")
	llm.PersistDNS(env("DNS_CACHE", `data\dns-cache.json`))
	a := agent.New(cfg,
		mcp.New(env("MCP_URL", "http://127.0.0.1:25350/mcp"), token),
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
	if env("VOICE", "true") == "true" {
		heard := a.WithVoice(voice.NewSpeaker(env("VOICE_SAY_URL", "http://127.0.0.1:8091/say")))
		go func() {
			if err := voice.Listen(ctx, env("VOICE_LISTEN", "127.0.0.1:8092"), heard); err != nil {
				log.Error("voice listener", "err", err)
			}
		}()
	}
	log.Info("pyhtun starting", "bot", cfg.BotNick, "creative", cfg.Creative)
	if err := a.Run(ctx); err != nil {
		fatal(log, "agent stopped", "err", err)
	}
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
