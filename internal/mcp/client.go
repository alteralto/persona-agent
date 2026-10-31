// Package mcp is a minimal JSON-RPC client for the MCP server hosted by the UnionClef mod.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	url   string
	token string
	http  *http.Client
	id    atomic.Int64

	// Finding the client by the player's name (two game clients on one machine).
	player   string
	ports    []int
	mu       sync.Mutex
	found    string // the url found, "" until found or after it stopped answering
	lookedAt time.Time
}

func New(url, token string) *Client {
	return &Client{url: url, token: token, http: &http.Client{Timeout: 10 * time.Second}}
}

// ForPlayer finds the game client of one player among the mod servers on 127.0.0.1 at ports
// from..from+9 (the mod takes the next free port when another client has its own), by the name in
// getGameState, and looks again whenever that one stops answering (a game restart can swap them).
func ForPlayer(player, token string, from int) *Client {
	c := New("", token)
	c.player = player
	for p := from; p < from+10; p++ {
		c.ports = append(c.ports, p)
	}
	return c
}

func (c *Client) target(ctx context.Context) (string, error) {
	if c.player == "" {
		return c.url, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.found != "" {
		return c.found, nil
	}
	if time.Since(c.lookedAt) < 2*time.Second {
		return "", fmt.Errorf("no game client of %s found", c.player)
	}
	c.lookedAt = time.Now()
	var nameless []string // an older mod, or not in a world yet: no name to go by
	for _, p := range c.ports {
		url := fmt.Sprintf("http://127.0.0.1:%d/mcp", p)
		qctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		text, err := c.call(qctx, url, "getGameState", nil)
		cancel()
		if err != nil {
			continue
		}
		var st struct {
			Self struct {
				Name string `json:"name"`
			} `json:"self"`
		}
		_ = json.Unmarshal([]byte(text), &st)
		switch {
		case strings.EqualFold(st.Self.Name, c.player):
			c.found = url
			return url, nil
		case st.Self.Name == "":
			nameless = append(nameless, url)
		}
	}
	if len(nameless) > 0 {
		return nameless[0], nil // not cached: look again once the name shows up
	}
	return "", fmt.Errorf("no game client of %s found", c.player)
}

func (c *Client) lost() {
	if c.player != "" {
		c.mu.Lock()
		c.found = ""
		c.mu.Unlock()
	}
}

// GameDir is the vanilla launcher's game folder, %APPDATA%\.minecraft. Another launcher keeps it
// elsewhere: set MC_SETTINGS and MARKS_DIR then.
func GameDir() string { return filepath.Join(os.Getenv("APPDATA"), ".minecraft") }

// DefaultSettings is the mod's settings file in GameDir.
func DefaultSettings() string { return filepath.Join(GameDir(), "altoclef", "altoclef_settings.json") }

// TokenFromSettings reads mcpAuthToken from altoclef_settings.json without logging it.
func TokenFromSettings(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var s struct {
		Token string `json:"mcpAuthToken"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if s.Token == "" {
		return "", fmt.Errorf("mcpAuthToken is empty in %s (start the game once with the mod)", path)
	}
	return s.Token, nil
}

type rpcResponse struct {
	Result *struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Call invokes a tool and returns its text content.
func (c *Client) Call(ctx context.Context, tool string, args map[string]any) (string, error) {
	url, err := c.target(ctx)
	if err != nil {
		return "", err
	}
	text, err := c.call(ctx, url, tool, args)
	if err != nil && ctx.Err() == nil && isConnErr(err) {
		c.lost()
	}
	return text, err
}

func isConnErr(err error) bool {
	var ne net.Error
	var oe *net.OpError
	return errors.As(err, &oe) || errors.As(err, &ne)
}

func (c *Client) call(ctx context.Context, url, tool string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.id.Add(1),
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mcp %s: http %d", tool, resp.StatusCode)
	}
	var r rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("mcp %s: %w", tool, err)
	}
	if r.Error != nil {
		return "", fmt.Errorf("mcp %s: %d %s", tool, r.Error.Code, r.Error.Message)
	}
	if r.Result == nil {
		return "", fmt.Errorf("mcp %s: empty result", tool)
	}
	text := ""
	if len(r.Result.Content) > 0 {
		text = r.Result.Content[0].Text
	}
	if r.Result.IsError {
		return text, fmt.Errorf("mcp %s: %s", tool, text)
	}
	return text, nil
}
