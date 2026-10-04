// layatune improves the option descriptions Laya scores, and only keeps a change that wins on
// examples it never saw.
//
// The bench (bench/laya_cases.json) is split in two: train (even rows) and test (odd rows).
// Each round GigaChat sees the train mistakes and rewrites the descriptions; a rewrite is kept
// only if train accuracy goes up. At the end the best set is scored on test, and written to
// data/laya_criteria.json (which the agent loads at start) only if it beats the current set there.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/giga"
	"github.com/alteralto/persona-agent/internal/laya"
)

type benchCase struct {
	Text  string `json:"text"`
	Label string `json:"label"`
}

type miss struct {
	Text, Want, Got string
}

func main() {
	cases := flag.String("cases", `bench\laya_cases.json`, "labelled lines")
	out := flag.String("out", `data\laya_criteria.json`, "where the winning criteria go")
	rounds := flag.Int("rounds", 4, "rewrite attempts")
	model := flag.String("model", "GigaChat-2-Max", "model that rewrites the descriptions")
	minProb := flag.Float64("minprob", 0.45, "agent's acceptance threshold")
	dry := flag.Bool("dry", false, "score only, never write")
	auto := flag.String("auto", `bench\laya_cases.auto.json`, "weak-labelled lines: added to train only")
	flag.Parse()
	loadDotEnv(".env")
	ctx := context.Background()

	raw, err := os.ReadFile(*cases)
	must(err)
	var all []benchCase
	must(json.Unmarshal(raw, &all))
	var train, test []benchCase
	for i, c := range all {
		if i%2 == 0 {
			train = append(train, c)
		} else {
			test = append(test, c)
		}
	}
	// Weak labels (benchmine) only train; the test half stays hand-labelled so the gate is honest.
	if raw, err := os.ReadFile(*auto); err == nil {
		var extra []benchCase
		if json.Unmarshal(raw, &extra) == nil && len(extra) > 0 {
			train = append(train, extra...)
			fmt.Printf("train += %d weak-labelled lines from %s\n", len(extra), *auto)
		}
	}
	l := laya.New(envOr("LAYA_URL", "http://127.0.0.1:8090"))

	current := agent.DefaultCriteria()
	if ok, err := agent.LoadCriteria(*out); err != nil {
		fmt.Println("tuned file unreadable, starting from defaults:", err)
	} else if ok {
		current = agent.CurrentCriteria()
		fmt.Println("starting from the tuned set in", *out)
	}
	if current.MinProb == 0 {
		current.MinProb = *minProb
	}

	// Laya is asked once per case and criteria set; rules are then searched on the cache.
	trainA, testA := collect(ctx, l, current, train), collect(ctx, l, current, test)
	baseTrain, missTrain := evalRule(trainA, train, current.Mode, current.MinProb)
	baseTest, _ := evalRule(testA, test, current.Mode, current.MinProb)
	fmt.Printf("current (%s, %.2f): train %.0f%%  test %.0f%%  (train %d, test %d cases)\n",
		modeName(current.Mode), current.MinProb, baseTrain*100, baseTest*100, len(train), len(test))
	printMisses(missTrain)

	best, bestTrain := current, baseTrain
	bestTrainA := trainA
	tryRules := func(c agent.Criteria, answers []map[string]laya.Answer, label string) {
		for _, mode := range []string{"", "ru", "en", "max"} {
			for p := 0.30; p <= 0.701; p += 0.05 {
				acc, _ := evalRule(answers, train, mode, p)
				if acc > bestTrain {
					c.Mode, c.MinProb = mode, p
					best, bestTrain, bestTrainA = c, acc, answers
					fmt.Printf("%s: rule %s %.2f -> train %.0f%%\n", label, modeName(mode), p, acc*100)
				}
			}
		}
	}
	tryRules(current, trainA, "rules")
	if *dry {
		report(ctx, l, best, bestTrain, test, baseTest, *out, true)
		return
	}

	llm := giga.New(os.Getenv("GIGACHAT_CREDENTIALS"), envOr("GIGACHAT_SCOPE", "GIGACHAT_API_PERS"), *model,
		os.Getenv("GIGACHAT_VERIFY_SSL") == "true")
	for r := 1; r <= *rounds; r++ {
		_, misses := evalRule(bestTrainA, train, best.Mode, best.MinProb)
		if len(misses) == 0 {
			break
		}
		cand, err := rewrite(ctx, llm, best, misses)
		if err != nil {
			fmt.Printf("round %d: rewrite failed: %v\n", r, err)
			continue
		}
		cand.Mode, cand.MinProb = best.Mode, best.MinProb
		ca := collect(ctx, l, cand, train)
		acc, _ := evalRule(ca, train, cand.Mode, cand.MinProb)
		fmt.Printf("round %d: train %.0f%% (best %.0f%%)\n", r, acc*100, bestTrain*100)
		if acc > bestTrain {
			best, bestTrain, bestTrainA = cand, acc, ca
		}
		tryRules(cand, ca, fmt.Sprintf("round %d", r))
	}
	report(ctx, l, best, bestTrain, test, baseTest, *out, false)
}

// report scores the winner on the held-out half and writes it only if it beats the current set.
func report(ctx context.Context, l *laya.Client, best agent.Criteria, bestTrain float64, test []benchCase,
	baseTest float64, out string, dry bool) {
	bestTest, misses := evalRule(collect(ctx, l, best, test), test, best.Mode, best.MinProb)
	fmt.Printf("best (%s, %.2f): train %.0f%%  test %.0f%%  vs current test %.0f%%\n",
		modeName(best.Mode), best.MinProb, bestTrain*100, bestTest*100, baseTest*100)
	printMisses(misses)
	if dry {
		return
	}
	if bestTest <= baseTest {
		fmt.Println("no gain on unseen cases: keeping the current criteria")
		return
	}
	data, _ := json.MarshalIndent(best, "", "  ")
	must(os.MkdirAll("data", 0o755))
	must(os.WriteFile(out, data, 0o644))
	fmt.Println("written", out, "— the agent picks it up on next start")
}

func modeName(m string) string {
	if m == "" {
		return "agree"
	}
	return m
}

// collect asks Laya about every case once.
func collect(ctx context.Context, l *laya.Client, c agent.Criteria, cases []benchCase) []map[string]laya.Answer {
	q := agent.QuestionsWith("Pyhtun", c)
	out := make([]map[string]laya.Answer, len(cases))
	for i, bc := range cases {
		for try := 0; try < 3; try++ {
			a, err := l.Decide(ctx, agent.Situation("Pyhtun", "player", bc.Text), q)
			if err == nil {
				out[i] = a
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	return out
}

// evalRule scores the full decision the agent makes: Laya under a rule, then keyword rules.
func evalRule(answers []map[string]laya.Answer, cases []benchCase, mode string, minProb float64) (float64, []miss) {
	ok := 0
	var misses []miss
	for i, bc := range cases {
		got := "error"
		if answers[i] != nil {
			got = agent.ApplyRules(agent.DecideWith(answers[i], mode, minProb), bc.Text).Action
		}
		if got == bc.Label {
			ok++
		} else {
			misses = append(misses, miss{bc.Text, bc.Label, got})
		}
	}
	return float64(ok) / float64(len(cases)), misses
}

func rewrite(ctx context.Context, llm *giga.Client, c agent.Criteria, misses []miss) (agent.Criteria, error) {
	cur, _ := json.MarshalIndent(c, "", "  ")
	var b strings.Builder
	for _, m := range misses {
		fmt.Fprintf(&b, "- %q: нужно %s, выбрано %s\n", m.Text, m.Want, m.Got)
	}
	prompt := "Классификатор выбирает действие игрового бота по фразе игрока, сравнивая фразу с описаниями " +
		"вариантов на русском (ru) и английском (en); действие принимается, только если оба языка выбрали одно и то же. " +
		"Текущие описания:\n" + string(cur) + "\n\nОшибки на обучающих фразах:\n" + b.String() +
		"\nПерепиши описания так, чтобы исправить ошибки и не сломать остальное. Ключи не меняй. " +
		"Каждое описание — одна конкретная фраза до 120 символов: когда выбирать вариант и чем он отличается от соседних. " +
		"Помни: wait — когда игрок просто сообщает, где стоит, свои координаты, рассказывает или говорит с другим. " +
		"Ответь только JSON вида {\"ru\":{...},\"en\":{...}} без пояснений."
	// One stream per key, shared with the running agent: back off on 429 instead of giving up.
	var text string
	var err error
	for try, wait := 0, 3*time.Second; try < 6; try, wait = try+1, wait*2 {
		if text, err = llm.Chat(ctx, []giga.Message{{Role: "user", Content: prompt}}, 1200); err == nil {
			break
		}
		if !strings.Contains(err.Error(), "429") {
			return c, err
		}
		time.Sleep(wait)
	}
	if err != nil {
		return c, err
	}
	i := strings.Index(text, "{")
	if i < 0 {
		return c, fmt.Errorf("no JSON in reply")
	}
	// Decode reads the first object and ignores what follows: the model sometimes adds a
	// stray "}" or a comment after the JSON.
	var nc agent.Criteria
	if err := json.NewDecoder(strings.NewReader(text[i:])).Decode(&nc); err != nil {
		return c, err
	}
	for k := range c.RU {
		if nc.RU[k] == "" || nc.EN[k] == "" {
			return c, fmt.Errorf("rewrite dropped %q", k)
		}
	}
	return nc, nil
}

func printMisses(ms []miss) {
	for _, m := range ms {
		t := m.Text
		if r := []rune(t); len(r) > 70 {
			t = string(r[:70]) + "…"
		}
		fmt.Printf("   want %-6s got %-6s %s\n", m.Want, m.Got, t)
	}
}

func must(err error) {
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

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
		if k, v, ok := strings.Cut(line, "="); ok {
			if _, set := os.LookupEnv(strings.TrimSpace(k)); !set {
				os.Setenv(strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`))
			}
		}
	}
}
