package main

import (
	"context"
	"fmt"

	"github.com/alteralto/persona-agent/internal/agent"
	"github.com/alteralto/persona-agent/internal/laya"
)

func raw(l *laya.Client, text string) {
	a, _ := l.Decide(context.Background(), "Сейчас игрок alteralto пишет: "+text, agent.Questions("Pyhtun"))
	for _, k := range []string{"act_ru", "act_en", "say"} {
		fmt.Printf("   %s=%s %.2f %v\n", k, a[k].Choice, a[k].Confidence, a[k].Probabilities)
	}
}
