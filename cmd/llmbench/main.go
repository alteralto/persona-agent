// llmbench compares GigaChat models on the same character lines: latency and wording.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/giga"
)

func main() {
	loadDotEnv(".env")
	base := giga.New(os.Getenv("GIGACHAT_CREDENTIALS"), envOr("GIGACHAT_SCOPE", "GIGACHAT_API_PERS"), "GigaChat",
		os.Getenv("GIGACHAT_VERIFY_SSL") == "true")
	ctx := context.Background()
	var models []string
	var err error
	for try := 0; try < 6; try++ { // the local DNS resolver times out now and then
		if models, err = base.Models(ctx); err == nil {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		fmt.Println("models:", err)
		return
	}
	fmt.Println("available:", strings.Join(models, ", "))
	if len(os.Args) > 1 {
		models = os.Args[1:]
	}
	for _, m := range models {
		if strings.Contains(strings.ToLower(m), "embed") {
			continue
		}
		c := base.WithModel(m)
		fmt.Printf("\n=== %s\n", m)
		for _, s := range agent.BenchScenes() {
			for try := 0; try < 2; try++ { // second call shows warm latency
				start := time.Now()
				out, err := c.Chat(ctx, s.Messages, 120)
				ms := time.Since(start).Milliseconds()
				if err != nil {
					fmt.Printf("  [%s] error: %v\n", s.Name, err)
					break
				}
				if try == 1 {
					fmt.Printf("  [%s] %d ms: %s\n", s.Name, ms, agent.CleanReply(out))
				}
			}
		}
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
