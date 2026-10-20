// simrun plays a scripted scene against the simulated game and prints what the bot did and
// said. With -llm=real it uses GigaChat from .env and with -laya the real laya-serve, so reply
// quality and decisions can be checked without Minecraft.
//
//	go run ./cmd/simrun -llm real -laya http://127.0.0.1:8090 -for 3m
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/giga"
	"github.com/alteralto/persona-agent/internal/laya"
	"github.com/alteralto/persona-agent/internal/mcp"
	"github.com/alteralto/persona-agent/internal/sim"
)

type line struct {
	at   time.Duration
	nick string
	text string
}

// The scene: the bot at home with the two other bots nearby and a player far away.
var script = []line{
	{2 * time.Second, "NetTyan", "Привет, Пыхтун! Как поживает твоя нора?"},
	{9 * time.Second, "Pon4ikYT", "Нетян, пойдём к озеру?"},
	{14 * time.Second, "Alex", "Пыхтун, где тут тыквенная ферма?"},
	{22 * time.Second, "Alex", "Пыхтун, нужны ракеты, лечу на север"},
	{32 * time.Second, "Alex", "Пыхтун, прилетай ко мне"},
	{45 * time.Second, "Pon4ikYT", "Пыхтун, а ты боишься криперов?"},
	{55 * time.Second, "Alex", "Пыхтун, стой тут"},
	{65 * time.Second, "NetTyan", "Пыхтун, расскажи что-нибудь интересное"},
	{80 * time.Second, "Alex", "Пыхтун, ты дурак"},
	{95 * time.Second, "Pon4ikYT", "Пыхтун, полетели вместе!"},
	{110 * time.Second, "Alex", "Пыхтун, отведи меня к норе"},
}

func main() {
	llmMode := flag.String("llm", "fake", "fake | real (GigaChat from .env)")
	layaURL := flag.String("laya", "", "real laya-serve URL; empty = stand-in (rules decide)")
	dur := flag.Duration("for", 2*time.Minute, "how long to run")
	out := flag.String("out", "", "also write the timeline here")
	flag.Parse()
	loadDotEnv(".env")

	w := sim.New(sim.Options{BotNick: "arcmlchikkk", Mode: "creative", Fork: true, FreeSlots: 3})
	w.AddPlayer("NetTyan", 8, 3)
	w.AddPlayer("Pon4ikYT", -6, 10)
	w.AddPlayer("Alex", 260, -90)
	w.AddSign(40, 70, 30, "Тыквенная ферма")
	w.AddSign(-30, 70, -50, "Розовый дом Нетян")
	w.AddBuilding(-30, -50, 6, "Розовая шерсть")
	w.AddBuilding(60, 70, 5, "Стекло")
	game := httptest.NewServer(w.Handler())
	defer game.Close()

	var layaClient *laya.Client
	if *layaURL != "" {
		layaClient = laya.New(*layaURL)
	} else {
		ls := httptest.NewServer(sim.Laya())
		defer ls.Close()
		layaClient = laya.New(ls.URL)
	}

	var llm *giga.Client
	fake := &sim.Giga{}
	if *llmMode == "real" {
		creds := os.Getenv("GIGACHAT_CREDENTIALS")
		if creds == "" {
			fmt.Fprintln(os.Stderr, "GIGACHAT_CREDENTIALS missing in .env")
			os.Exit(1)
		}
		llm = giga.New(creds, "GIGACHAT_API_PERS", "GigaChat-2", false)
		llm.PersistDNS(`data\dns-cache.json`)
	} else {
		gs := httptest.NewServer(fake.Handler())
		defer gs.Close()
		llm = giga.New("sim", "GIGACHAT_API_PERS", "GigaChat-2", true).
			WithEndpoints(gs.URL+"/oauth", gs.URL+"/chat/completions")
	}

	tmp, _ := os.MkdirTemp("", "simrun")
	defer os.RemoveAll(tmp)
	cfg := agent.Config{
		BotNick:         "arcmlchikkk",
		Aliases:         []string{"пыхтун", "арк"},
		OtherBots:       []string{"NetTyan", "Pon4ikYT"},
		Companions:      []string{"NetTyan"},
		OthersNames:     []string{"пончик", "пон", "pon4ik", "pon4ikyt", "nettyan", "нетян", "неттян"},
		Creative:        true,
		MinConfidence:   0.45,
		PollEvery:       500 * time.Millisecond,
		RequireMention:  true,
		ReplyEvery:      3 * time.Second,
		IdleEvery:       40 * time.Second,
		ExploreAfter:    20 * time.Second,
		ActivityEvery:   30 * time.Second,
		PlacesPath:      filepath.Join(tmp, "places.json"),
		PeopleDir:       filepath.Join(tmp, "people"),
		ProtectRadius:   256,
		AutoCreative:    true,
		CreativeCommand: "/gamemode creative",
	}
	if tuned, err := agent.LoadCriteria(`data\laya_criteria.json`); err == nil && tuned && *layaURL != "" {
		fmt.Println("laya criteria: tuned set")
	}
	logFile, _ := os.Create(filepath.Join(tmp, "agent.log"))
	a := agent.New(cfg, mcp.New(game.URL, "sim"), layaClient, llm, slog.New(slog.NewTextHandler(logFile, nil)))
	if *llmMode == "real" {
		a.WithSmartModel(llm.WithModel("GigaChat-2-Max"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()
	go func() { _ = a.Run(ctx) }()
	start := time.Now()
	for _, l := range script {
		select {
		case <-ctx.Done():
		case <-time.After(time.Until(start.Add(l.at))):
			w.Say(l.nick, l.text)
		}
	}
	<-ctx.Done()
	time.Sleep(300 * time.Millisecond)

	var sink io.Writer = os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err == nil {
			defer f.Close()
			sink = io.MultiWriter(os.Stdout, f)
		}
	}
	var said []string
	for _, e := range w.Events() {
		if e.Kind == "say" {
			said = append(said, e.Text)
		}
		if e.Kind == "tool" && !strings.HasPrefix(e.Text, "creativeGive") && !strings.HasPrefix(e.Text, "tidy") &&
			!strings.HasPrefix(e.Text, "setFavorite") {
			continue
		}
		fmt.Fprintf(sink, "%6.1fs %-4s %s\n", e.At.Seconds(), e.Kind, e.Text)
	}
	fmt.Fprintf(sink, "\nquality: %s\n", agent.ReplyQuality(said))
	// Decisions from the agent log, for checking Laya and the rules.
	logFile.Close()
	if raw, err := os.ReadFile(filepath.Join(tmp, "agent.log")); err == nil {
		fmt.Fprintln(sink, "\n--- decisions and warnings ---")
		for _, l := range strings.Split(string(raw), "\n") {
			if strings.Contains(l, `msg=decision`) || strings.Contains(l, "level=WARN") || strings.Contains(l, "critic") {
				fmt.Fprintln(sink, l)
			}
		}
	}
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}
