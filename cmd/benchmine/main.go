// benchmine turns new lines from the decision journal into bench candidates: it drops what the
// bench already has, asks GigaChat for a label, and writes them to a separate file. These
// weak labels only ever feed the training half of a tuning run; the test half stays
// hand-labelled, so a wrong weak label cannot sneak a regression past the gate.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/giga"
)

type benchCase struct {
	Text  string `json:"text"`
	Label string `json:"label"`
	Src   string `json:"src"`
}

var labels = map[string]string{
	"wait":   "ничего не делать: болтовня, вопрос, игрок сообщает свои координаты или говорит с другим",
	"follow": "игрок просит подойти к нему или идти за ним",
	"stop":   "игрок просит остановиться, подождать на месте",
	"defend": "рядом опасность: мобы, нападение",
	"supply": "игрок просит принести ресурсы: бумагу, порох, ракеты, фейерверки",
	"lead":   "игрок просит отвести его к норе, показать дорогу",
}

func main() {
	journal := flag.String("journal", `data\decisions.jsonl`, "decision journal")
	bench := flag.String("bench", `bench\laya_cases.json`, "hand-labelled bench")
	out := flag.String("out", `bench\laya_cases.auto.json`, "weak-labelled candidates")
	max := flag.Int("max", 40, "label at most this many new lines per run")
	model := flag.String("model", "GigaChat-2-Max", "labeller")
	flag.Parse()
	loadDotEnv(".env")
	ctx := context.Background()

	known := map[string]bool{}
	for _, path := range []string{*bench, *out} {
		var cs []benchCase
		if raw, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(raw, &cs)
		}
		for _, c := range cs {
			known[norm(c.Text)] = true
		}
	}
	var existing []benchCase
	if raw, err := os.ReadFile(*out); err == nil {
		_ = json.Unmarshal(raw, &existing)
	}

	f, err := os.Open(*journal)
	if err != nil {
		fmt.Println("no journal yet:", err)
		return
	}
	defer f.Close()
	var fresh []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		k := norm(e.Text)
		if len(strings.Fields(k)) < 3 || known[k] || seen[k] {
			continue
		}
		seen[k] = true
		fresh = append(fresh, e.Text)
	}
	if len(fresh) > *max {
		fresh = fresh[len(fresh)-*max:]
	}
	fmt.Printf("%d new lines to label\n", len(fresh))
	if len(fresh) == 0 {
		return
	}

	llm := giga.New(os.Getenv("GIGACHAT_CREDENTIALS"), envOr("GIGACHAT_SCOPE", "GIGACHAT_API_PERS"), *model,
		os.Getenv("GIGACHAT_VERIFY_SSL") == "true")
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var defs strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&defs, "%s — %s\n", k, labels[k])
	}
	added := 0
	for _, text := range fresh {
		prompt := "Фраза игрока в Minecraft боту по имени Пыхтун:\n«" + text + "»\n\nВыбери одно действие бота:\n" +
			defs.String() + "\nОтветь одним словом — ключом действия."
		reply, err := chatWithBackoff(ctx, llm, prompt)
		if err != nil {
			fmt.Println("label failed:", err)
			continue
		}
		label := strings.ToLower(strings.Trim(strings.TrimSpace(reply), ".«»\"' "))
		if _, ok := labels[label]; !ok {
			fmt.Printf("  skip (unclear label %q): %s\n", reply, text)
			continue
		}
		existing = append(existing, benchCase{Text: text, Label: label, Src: "auto"})
		added++
		fmt.Printf("  %-6s %s\n", label, text)
	}
	data, _ := json.MarshalIndent(existing, "", " ")
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Println("write:", err)
		return
	}
	fmt.Printf("added %d weak-labelled lines to %s (review them; they only train, never test)\n", added, *out)
}

func chatWithBackoff(ctx context.Context, llm *giga.Client, prompt string) (string, error) {
	var text string
	var err error
	for try, wait := 0, 3*time.Second; try < 5; try, wait = try+1, wait*2 {
		if text, err = llm.Chat(ctx, []giga.Message{{Role: "user", Content: prompt}}, 10); err == nil {
			return text, nil
		}
		if !strings.Contains(err.Error(), "429") {
			return "", err
		}
		time.Sleep(wait)
	}
	return "", err
}

func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

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
