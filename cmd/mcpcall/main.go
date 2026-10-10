// mcpcall calls one tool of the mod's MCP server and prints the result: a debugging aid for
// live sessions. The token is read from the mod's settings, never printed.
//
//	go run ./cmd/mcpcall getGameState
//	go run ./cmd/mcpcall flyTo '{"x":0,"y":70,"z":0,"low":true}'
//	MC_PLAYER=arca_pro go run ./cmd/mcpcall getGameState   # the client of that player
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/alteralto/persona-agent/internal/mcp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mcpcall <tool> [json-args]")
		os.Exit(2)
	}
	settings := os.Getenv("MC_SETTINGS")
	if settings == "" {
		settings = `F:\.tlauncher\legacy\Minecraft\game\altoclef\altoclef_settings.json`
	}
	token, err := mcp.TokenFromSettings(settings)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read MCP token:", err)
		os.Exit(1)
	}
	args := map[string]any{}
	if len(os.Args) > 2 {
		if err := json.Unmarshal([]byte(os.Args[2]), &args); err != nil {
			fmt.Fprintln(os.Stderr, "args:", err)
			os.Exit(2)
		}
	}
	url := os.Getenv("MCP_URL")
	if url == "" {
		url = "http://127.0.0.1:25350/mcp"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := mcp.New(url, token)
	if p := os.Getenv("MC_PLAYER"); p != "" && os.Getenv("MCP_URL") == "" {
		c = mcp.ForPlayer(p, token, 25350)
	}
	out, err := c.Call(ctx, os.Args[1], args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(out)
}
