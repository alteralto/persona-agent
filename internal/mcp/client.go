// Package mcp is a minimal JSON-RPC client for the MCP server hosted by the UnionClef mod.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

type Client struct {
	url   string
	token string
	http  *http.Client
	id    atomic.Int64
}

func New(url, token string) *Client {
	return &Client{url: url, token: token, http: &http.Client{Timeout: 10 * time.Second}}
}

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
	if args == nil {
		args = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.id.Add(1),
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
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
