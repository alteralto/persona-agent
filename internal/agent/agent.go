// Package agent is the decision loop: chat in, Laya decides, UnionClef acts, GigaChat speaks.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/alteralto/persona-agent/internal/chatlog"
	"github.com/alteralto/persona-agent/internal/explore"
	"github.com/alteralto/persona-agent/internal/giga"
	"github.com/alteralto/persona-agent/internal/laya"
	"github.com/alteralto/persona-agent/internal/mcp"
	"github.com/alteralto/persona-agent/internal/memory"
	"github.com/alteralto/persona-agent/internal/voice"
)

type Config struct {
	BotNick       string
	Aliases       []string // names players use in chat, e.g. "пыхтун"
	OtherBots     []string // other AI characters: limit back-and-forth with them
	Creative      bool
	MinConfidence float64
	PollEvery     time.Duration
	MaxBotReplies int
	// RequireMention: react only to lines that name the bot. On a busy server
	// players' "stop"/"follow me" to each other must not drive the bot.
	RequireMention bool
	ReplyEvery     time.Duration
	// IdleEvery: when nobody has talked to the bot for this long, it says something
	// on its own. Zero disables it.
	IdleEvery time.Duration
	Intro     bool // greet the server on start
	// Explore: walk unvisited chunks around home when nobody needs the bot.
	Explore       bool
	ExploreRadius int           // in chunks around home
	ExploreAfter  time.Duration // quiet time after the last mention before roaming
	PlacesPath    string
	// Companions: when idle, stay next to the first of these players instead of exploring.
	Companions []string
	// JournalPath: every decision as a JSON line, for bench mining (empty: off).
	JournalPath string
	// PeopleDir: one memory file per person the bot talks to (empty: off).
	PeopleDir string
	// AutoCreative: on this server the bot asks for creative mode with CreativeCommand.
	// ActivityEvery: start something on the bot's own initiative this often (0 = off).
	ActivityEvery   time.Duration
	AutoCreative    bool
	CreativeCommand string
	// OthersNames: how players call the other bots ("пончик", "nettyan"). A bot line that
	// opens with one of these is addressed to someone else, and the bot keeps quiet.
	OthersNames []string
	// ProtectRadius: blocks around home where the mod may neither break nor place anything.
	// Pathfinding otherwise digs through walls, which on a shared server is griefing.
	ProtectRadius int
}

type Agent struct {
	cfg  Config
	mc   *mcp.Client
	laya *laya.Client
	llm  *giga.Client
	// smart answers questions about places and lines that describe a real action: it is
	// ~300 ms slower but does not invent directions. Chatter stays on the fast model.
	smart *giga.Client
	log   *slog.Logger

	history    []string // recent player lines, "Nick: text"
	following  string
	botReplies int
	lastReply  time.Time
	lastSpoke  atomic.Int64 // unix nanos of the last chat line the bot sent
	replyQueue chan replyJob

	places      *explore.Places
	trip        *trip
	pos         explore.Pos
	havePos     bool
	lastCommand time.Time

	outing     *outing
	protected  bool
	ownLines   []string // last lines the bot said; touched only by replyWorker
	lastTopic  int
	stillPos   explore.Pos
	stillSince time.Time
	leading    bool // walking players to the burrow

	// Fork features (see modtools.go).
	creative        bool // from getGameState when the fork reports it, else from config
	modeKnown       bool
	modeChecked     time.Time
	fork            bool // setBuildPolicy worked: the fork's tools are there
	flightKit       bool
	noFlight        bool
	noTidy          bool
	botsNear        []string
	botsKnown       bool
	outOfWorld      bool
	followIdleSince time.Time
	flying          bool
	flightStarted   time.Time

	journal *journal
	people  *memory.Store

	creativeAsked time.Time

	activity     *activity
	lastActivity time.Time
	lastRequest  time.Time // last time a player asked for an action
	readSigns    map[string]bool
	flyTarget    string // player named in "лети к ..."; empty means the speaker
	followAfter  string // follow this player on foot once the flight lands
	// lingerUntil: stay put (no companion follow) until then. A separate clock, because every
	// line the other bots say refreshes lastCommand and used to cut the wait short.
	lingerUntil  time.Time
	fallSaves    bool
	fallSavesSet bool

	speaker *voice.Speaker   // nil: text chat only
	heard   chan voice.Heard // recognized player speech
}

type replyJob struct {
	prompt   string // set for lines the bot starts itself (intro, idle remark)
	history  []string
	nick     string
	text     string
	action   string
	places   string // known places near the bot, for "where is ..." questions
	creative bool
	at       time.Time
}

func New(cfg Config, mc *mcp.Client, l *laya.Client, llm *giga.Client, log *slog.Logger) *Agent {
	a := &Agent{cfg: cfg, mc: mc, laya: l, llm: llm, log: log, replyQueue: make(chan replyJob, 1),
		heard: make(chan voice.Heard, 4), creative: cfg.Creative, journal: newJournal(cfg.JournalPath), readSigns: map[string]bool{}}
	if cfg.PeopleDir != "" {
		a.people = memory.New(cfg.PeopleDir)
	}
	return a
}

// WithSmartModel sets the slower, more accurate model for factual lines.
func (a *Agent) WithSmartModel(c *giga.Client) { a.smart = c }

// asksWhere reports whether a line asks about a place or a direction.
func asksWhere(text string) bool {
	t := strings.ToLower(text)
	for _, w := range []string{"где", "куда", "откуда", "сколько", "далеко", "координат", "направлен", "дорог", "клетк"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// WithVoice makes the bot speak its lines and returns the channel for recognized speech.
func (a *Agent) WithVoice(s *voice.Speaker) chan<- voice.Heard {
	a.speaker = s
	return a.heard
}

func (a *Agent) Run(ctx context.Context) error {
	places, err := explore.LoadPlaces(a.cfg.PlacesPath)
	if err != nil {
		return err
	}
	a.places = places
	a.lastCommand = time.Now()
	go func() {
		// The local resolver sometimes times out; keep trying so the address cache and the
		// token are ready before the first reply needs them.
		for attempt := 1; ; attempt++ {
			start := time.Now()
			err := a.llm.Warm(ctx)
			if err == nil {
				a.log.Info("gigachat token ready", "ms", time.Since(start).Milliseconds(), "attempt", attempt)
				return
			}
			a.log.Warn("gigachat warm-up", "err", err, "attempt", attempt)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	go a.replyWorker(ctx)
	if a.people != nil {
		go a.summarizer(ctx)
	}

	// The agent may start before the game: wait for the mod instead of exiting.
	prev, err := a.recentChat(ctx)
	for waited := false; err != nil; prev, err = a.recentChat(ctx) {
		if !waited {
			waited = true
			a.log.Info("waiting for the game", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(3 * time.Second):
		}
	}
	a.log.Info("connected to game", "chat_lines", len(prev))
	if a.cfg.Intro {
		a.enqueue(replyJob{prompt: introPrompt, at: time.Now()})
	}
	a.lastSpoke.Store(time.Now().UnixNano())
	var lastLine string
	var lastAt time.Time
	gameDown := false
	tick := 0
	t := time.NewTicker(a.cfg.PollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case h := <-a.heard:
			a.log.Info("heard", "nick", h.Nick, "text", h.Text, "stt_ms", h.STTms)
			a.handleMessage(ctx, chatlog.Message{Nick: h.Nick, Text: h.Text}, "voice")
			continue
		case <-t.C:
		}
		cur, err := a.recentChat(ctx)
		if err != nil {
			// The game is closed or restarting: say so once, not twice a second.
			if !gameDown {
				gameDown = true
				a.log.Warn("game connection lost, waiting", "err", err)
			}
			continue
		}
		if gameDown {
			gameDown = false
			a.log.Info("game connection back")
			a.resetModState()
			prev = cur // lines from before the restart are not new
			continue
		}
		for _, line := range chatlog.NewLines(prev, cur) {
			// The mod records every incoming message twice; handle each once.
			if line == lastLine && time.Since(lastAt) < 2*time.Second {
				continue
			}
			lastLine, lastAt = line, time.Now()
			a.handleLine(ctx, line)
		}
		prev = cur
		if tick++; tick%4 == 0 { // every 2 s
			a.roam(ctx)
		}
		if a.cfg.IdleEvery > 0 && time.Since(time.Unix(0, a.lastSpoke.Load())) > a.cfg.IdleEvery {
			a.lastSpoke.Store(time.Now().UnixNano()) // one idle remark per period even if it fails
			a.idleTurn(ctx)
		}
	}
}

func (a *Agent) recentChat(ctx context.Context) ([]string, error) {
	text, err := a.mc.Call(ctx, "getRecentChat", map[string]any{"n": 40})
	if err != nil {
		return nil, err
	}
	var lines []string
	if json.Unmarshal([]byte(text), &lines) == nil {
		return lines, nil
	}
	text = strings.Trim(strings.TrimSpace(text), "[]")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, ", "), nil
}

func (a *Agent) handleLine(ctx context.Context, line string) {
	msg, ok := chatlog.Parse(line)
	if !ok {
		return
	}
	a.handleMessage(ctx, msg, "chat")
}

// handleMessage is shared by text chat and recognized speech.
func (a *Agent) handleMessage(ctx context.Context, msg chatlog.Message, source string) {
	if strings.EqualFold(msg.Nick, a.cfg.BotNick) || strings.HasPrefix(msg.Text, "@") {
		return
	}
	fromBot := slices.ContainsFunc(a.cfg.OtherBots, func(b string) bool { return strings.EqualFold(b, msg.Nick) })
	// Other AI characters talk to the bot without naming it; players must name it.
	if a.cfg.RequireMention && !fromBot && !a.mentioned(msg.Text) {
		a.remember(msg)
		return
	}
	// Speech recognition cuts bots' long monologues into scraps ("арк.", "Ветки по одной"):
	// a short line with no question and no name is noise, not a turn to answer.
	if fromBot && !a.mentioned(msg.Text) && len(words(msg.Text)) < 3 && !strings.Contains(msg.Text, "?") {
		a.remember(msg)
		return
	}
	if fromBot && !a.mentioned(msg.Text) && addressedTo(msg.Text, a.cfg.OthersNames) {
		a.log.Info("skip: addressed to someone else", "nick", msg.Nick)
		a.remember(msg)
		return
	}
	a.lastCommand = time.Now()
	if a.trip != nil {
		a.cancelTrip(ctx) // someone needs the bot: stop wandering and listen
	}
	if !fromBot {
		a.botReplies = 0
	}

	start := time.Now()
	answers, err := a.laya.Decide(ctx, situation(a.cfg.BotNick, a.history, msg.Nick, msg.Text), Questions(a.cfg.BotNick))
	if err != nil {
		a.log.Warn("laya", "err", err)
		return
	}
	d := decide(answers, a.cfg.MinConfidence)
	d, byRule := applyRules(d, msg.Text)
	a.log.Info("decision", "nick", msg.Nick, "text", msg.Text, "action", d.Action, "agreed", d.Agreed,
		"conf", round(d.ActionConf), "rule", byRule, "reply", d.Reply, "reply_conf", round(d.ReplyConf),
		"laya_ms", time.Since(start).Milliseconds())

	a.journal.record(msg.Nick, source, msg.Text, answers, d, byRule, fromBot)
	a.flyTarget = ""
	if d.Action == ActFlyTo {
		a.flyTarget = flyTargetWord(msg.Text)
	}
	if d.Action != ActWait {
		a.lastRequest = time.Now()
		a.activity = nil // a request beats the bot's own plans
	}
	a.act(ctx, d.Action, msg.Nick)

	// Answer when called by name or when a command was accepted (so the player hears it).
	if fromBot || a.mentioned(msg.Text) || d.Agreed && d.Action != ActWait {
		d.Reply = true
	}

	if d.Reply && fromBot && a.cfg.MaxBotReplies > 0 {
		if a.botReplies >= a.cfg.MaxBotReplies {
			a.log.Info("skip reply: bot-to-bot limit", "nick", msg.Nick)
			d.Reply = false
		} else {
			a.botReplies++
		}
	}
	if d.Reply {
		places := a.placesNote()
		if asksWhere(msg.Text) {
			places += a.signsNote(ctx)
		}
		a.enqueue(replyJob{history: slices.Clone(a.history), nick: msg.Nick, text: msg.Text, action: d.Action,
			places: places, creative: a.creative, at: start})
	}

	a.remember(msg)
}

// enqueue keeps only the newest job: GigaChat allows one stream, so a waiting job is
// replaced, never queued behind.
func (a *Agent) enqueue(job replyJob) {
	select {
	case <-a.replyQueue:
	default:
	}
	a.replyQueue <- job
}

// idlePrompt asks for a remark of the bot's own; with companions around it opens a conversation.
func (a *Agent) idlePrompt() string {
	bots := a.nearbyBots()
	if len(bots) == 0 {
		return idlePrompt
	}
	// A different topic each time, never the same twice in a row.
	i := rand.IntN(len(idleTopics) - 1)
	if i >= a.lastTopic {
		i++
	}
	a.lastTopic = i
	who := spokenName(bots[rand.IntN(len(bots))])
	return "Рядом с тобой " + who + ". Обратись к " + who + " по имени. Тема: " + idleTopics[i] + ". " +
		"Одна короткая фраза до 90 символов. Не выдумывай, что собеседник делает, если этого нет в чате."
}

var idleTopics = []string{
	"пожалуйся, что маг опять извёл все ракеты и придётся искать порох",
	"похвастайся запасами в норе: варенье, сыр, грибы",
	"вспомни постройку, которую недавно нашёл неподалёку, и спроси, чья она",
	"скажи что-нибудь про погоду или время суток, по-стариковски",
	"признайся, что боишься криперов, и попроси предупредить, если увидит",
	"расскажи короткую байку из своей молодости в Шире-подобной деревне",
	"спроси, что собеседник строит или ищет, и предложи помочь",
	"похвали собеседника за что-то конкретное из недавнего чата",
	"поворчи на высоту, уступы и тех, кто вечно лезет наверх",
	"предложи вместе сходить посмотреть окрестности, но без лишнего риска",
	"спроси совета, как лучше крафтить ракеты для мага",
}

// addressedTo reports whether one of the first words of text is one of names: "Пон, ..." is an
// address, "Поняла" is not.
func addressedTo(text string, names []string) bool {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) > 3 {
		words = words[:3]
	}
	for _, w := range words {
		for _, n := range names {
			if n != "" && w == strings.ToLower(n) {
				return true
			}
		}
	}
	return false
}

func (a *Agent) remember(msg chatlog.Message) {
	a.history = append(a.history, msg.Nick+": "+msg.Text)
	if len(a.history) > 8 {
		a.history = a.history[len(a.history)-8:]
	}
}

func (a *Agent) act(ctx context.Context, action, nick string) {
	var cmd string
	if action != ActWait {
		a.outing = nil // an explicit request overrides the bot's own walk
		a.leading = action == ActLead
	}
	switch action {
	case ActFollow:
		if a.following == nick {
			return
		}
		// Far away in creative: fly there first, then follow on foot.
		if a.creative && a.fork {
			if _, x, z, d, ok := a.playerPos(ctx, nick); ok && d > a.flyAbove() && a.startFlight(ctx, int(x), int(z)) {
				a.followAfter = nick
				return
			}
		}
		cmd, a.following = "@follow "+nick, nick
	case ActFlyTo:
		target := a.flyTarget
		if target == "" {
			target = nick
		}
		a.trip, a.following, a.activity = nil, "", nil
		if !a.flyToPlayer(ctx, target) && target == nick {
			cmd, a.following = "@follow "+nick, nick // cannot fly (survival, no kit): walk instead
			break
		}
		return
	case ActStop:
		cmd, a.following, a.followAfter = "@stop", "", ""
		// @stop halts walking only; the elytra controller flies on until told. The bot glides
		// down where it is, and checkFlight notices the flight has ended.
		if a.flying {
			if _, err := a.mc.Call(ctx, "flyStop", nil); err != nil {
				a.log.Warn("fly stop", "err", err)
			}
		}
		// "Wait here with me": stay a full minute before drifting back to the companion.
		a.lingerFor(time.Minute)
	case ActDefend:
		cmd = "@set mobDefense true"
	case ActLead:
		if a.places == nil || a.places.Home == nil {
			return
		}
		a.trip, a.following, a.leading = nil, "", true
		hx, hz := int(a.places.Home.X), int(a.places.Home.Z)
		// Hundreds of blocks from home in creative: fly, then walk the last metres (roam does it).
		if a.havePos && a.creative && math.Hypot(a.pos.X-float64(hx), a.pos.Z-float64(hz)) > 100 && a.startFlight(ctx, hx, hz) {
			return
		}
		cmd = fmt.Sprintf("@goto %d %d", hx, hz)
	case ActSupply:
		a.following = ""
		switch {
		case a.creative && a.fork:
			if cmd = a.supplyCreative(ctx, nick); cmd == "" {
				return
			}
		case a.creative:
			a.log.Info("supply skipped: creative needs the fork's creativeGive")
			return
		case a.fork:
			cmd = "@give " + nick + " firework_rocket 3" // fork knows the rocket recipe
		default:
			cmd = "@give " + nick + " paper 3"
		}
	case ActFly:
		a.trip, a.following = nil, ""
		// "Полетели вместе" from someone far away: fly to them, not off somewhere else.
		if _, _, _, d, ok := a.playerPos(ctx, nick); ok && d > a.flyAbove() && a.flyToPlayer(ctx, nick) {
			return
		}
		if !a.flyShowOff(ctx) {
			a.log.Info("fly skipped: no flight available")
		}
		return
	default:
		return
	}
	if _, err := a.mc.Call(ctx, "ExecuteCommand", map[string]any{"cmd": cmd}); err != nil {
		a.log.Warn("act", "cmd", cmd, "err", err)
		return
	}
	a.log.Info("act", "cmd", cmd)
}

func (a *Agent) replyWorker(ctx context.Context) {
	for {
		var job replyJob
		select {
		case <-ctx.Done():
			return
		case job = <-a.replyQueue:
		}
		action := job.action
		var note []giga.Message
		if job.prompt != "" {
			note = freeMessages(job.history, job.prompt)
		} else {
			noteKey := action
			if action == ActSupply && job.creative && a.fork {
				noteKey = noteSupplyCreative
			}
			note = replyMessages(job.history, job.nick, job.text, noteKey, job.places)
		}
		// What the bot remembers about this person goes first: notes and recent exchanges.
		if a.people != nil && job.nick != "" {
			if mem := a.people.Context(job.nick, 3); mem != "" {
				note[1].Content = mem + "\n" + note[1].Content
			}
		}
		// The bot's own recent lines: without them it repeats the same joke every minute.
		if len(a.ownLines) > 0 {
			note[1].Content = "Твои последние реплики — не повторяй их ни словами, ни темой:\n- " +
				strings.Join(a.ownLines, "\n- ") + "\n\n" + note[1].Content
		}
		if job.prompt == "" && action == ActSupply && job.creative && !a.fork {
			note[1].Content += "\n(Ты пока не умеешь брать вещи из креатив-вкладки — поворчи, что научишься позже.)"
		}
		llmStart := time.Now()
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		llm, model := a.llm, "fast"
		if a.smart != nil && (job.places != "" && asksWhere(job.text) || job.action != "" && job.action != ActWait) {
			llm, model = a.smart, "smart"
		}
		text, err := llm.Chat(cctx, note, 120)
		if err != nil && strings.Contains(err.Error(), "429") {
			// One stream per key: another request (a tuning run, the other model) holds it.
			time.Sleep(1500 * time.Millisecond)
			text, err = llm.Chat(cctx, note, 120)
		}
		cancel()
		if err != nil {
			a.log.Warn("gigachat", "err", err)
			continue
		}
		text = cleanReply(text)
		// Critic: a reply that promises movement while the bot stands still gets one rewrite
		// on the accurate model, told plainly that it is not moving.
		if job.prompt == "" && !moving(job.action) && promisesMove(text) {
			fixer := a.llm
			if a.smart != nil {
				fixer, model = a.smart, "smart-fix"
			}
			note[len(note)-1].Content += standStillFix
			fctx, fcancel := context.WithTimeout(ctx, 10*time.Second)
			fixed, err := fixer.Chat(fctx, note, 120)
			fcancel()
			if err == nil {
				a.log.Info("critic: rewrote moving promise", "was", text)
				text = cleanReply(fixed)
			}
		}
		if text == "" {
			continue
		}
		if wait := a.cfg.ReplyEvery - time.Since(a.lastReply); wait > 0 {
			time.Sleep(wait)
		}
		a.lastReply = time.Now()
		a.lastSpoke.Store(time.Now().UnixNano())
		if a.people != nil && job.nick != "" {
			if err := a.people.Append(job.nick, job.text, text); err != nil {
				a.log.Warn("people memory", "err", err)
			}
		}
		a.ownLines = append(a.ownLines, text)
		if len(a.ownLines) > 5 {
			a.ownLines = a.ownLines[len(a.ownLines)-5:]
		}
		if _, err := a.mc.Call(ctx, "ChatMessage", map[string]any{"msg": text}); err != nil {
			a.log.Warn("say", "err", err)
			continue
		}
		if a.speaker != nil {
			if err := a.speaker.Say(ctx, text); err != nil {
				a.log.Warn("voice", "err", err)
			}
		}
		a.log.Info("said", "model", model, "text", text, "llm_ms", time.Since(llmStart).Milliseconds(),
			"total_ms", time.Since(job.at).Milliseconds())
	}
}

func (a *Agent) mentioned(text string) bool {
	return mentions(text, append([]string{a.cfg.BotNick}, a.cfg.Aliases...))
}

// mentions matches names against the words of text. A long name also matches its declined
// forms ("пыхтуна", "пыхтуну"); a short one ("арк") must be the whole word, so "парк" and
// "арка" do not count.
func mentions(text string, names []string) bool {
	ws := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, name := range names {
		n := strings.ToLower(strings.TrimSpace(name))
		if n == "" {
			continue
		}
		for _, w := range ws {
			if w == n || len([]rune(n)) >= 5 && strings.HasPrefix(w, n) {
				return true
			}
		}
	}
	return false
}

// resetModState forgets everything the mod keeps only in the running client (build policy,
// fall saves, the flight kit, the game mode), so the next roam step sets it all again.
func (a *Agent) resetModState() {
	a.protected, a.fork, a.noFlight, a.noTidy = false, false, false, false
	a.fallSavesSet, a.flightKit, a.modeKnown, a.botsKnown = false, false, false, false
	a.modeChecked = time.Time{}
	a.flying, a.following, a.followAfter = false, "", ""
	a.trip, a.outing, a.activity, a.leading = nil, nil, nil, false
}

// lingerFor keeps the bot in place for at least d; a shorter wait never cuts a longer one.
func (a *Agent) lingerFor(d time.Duration) {
	if until := time.Now().Add(d); until.After(a.lingerUntil) {
		a.lingerUntil = until
	}
}

func round(f float64) float64 { return float64(int(f*1000)) / 1000 }
