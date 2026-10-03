// probe prints Laya decisions for sample chat lines (seed of the decision bench).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/laya"
)

func main() {
	lines := os.Args[1:]
	if len(lines) == 0 {
		lines = []string{
			"Пыхтун, иди за мной",
			"стой, хватит ходить за мной",
			"принеси пороха, мне ракеты нужны",
			"тут зомби, помогите!",
			"привет, как дела?",
			"вася, ты где строишь?",
		}
	}
	l := laya.New("http://127.0.0.1:8090")
	for _, text := range lines {
		start := time.Now()
		d, err := agent.Probe(context.Background(), l, "Pyhtun", "alteralto", text, 0.45)
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		if os.Getenv("RAW") != "" {
			raw(l, text)
		}
		fmt.Printf("%-40s action=%-7s agreed=%-5v conf=%.2f reply=%-5v (%.2f) %dms\n",
			text, d.Action, d.Agreed, d.ActionConf, d.Reply, d.ReplyConf, time.Since(start).Milliseconds())
	}
}
