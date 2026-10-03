// Package giga is a small GigaChat client: OAuth token refresh plus chat completions.
package giga

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	oauthURL = "https://ngw.devices.sberbank.ru:9443/api/v2/oauth"
	chatURL  = "https://gigachat.devices.sberbank.ru/api/v1/chat/completions"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client struct {
	dialer      *cachedDialer
	credentials string
	scope       string
	model       string
	http        *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New creates a client. verifyTLS=false skips certificate checks: GigaChat uses the Russian
// national CA, which is missing from the Windows trust store by default.
func New(credentials, scope, model string, verifyTLS bool) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// The local DNS resolver sometimes hangs for ~12 s; remember resolved addresses and keep
	// connections open so a reply almost never waits on DNS.
	dialer := &cachedDialer{dialer: &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second},
		ips: map[string]string{}}
	tr.DialContext = dialer.DialContext
	tr.IdleConnTimeout = 10 * time.Minute
	if !verifyTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		dialer:      dialer,
		credentials: credentials,
		scope:       scope,
		model:       model,
		http:        &http.Client{Timeout: 30 * time.Second, Transport: tr},
	}
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expires) > time.Minute {
		return c.token, nil
	}
	form := url.Values{"scope": {c.scope}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthURL, strings.NewReader(form))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("RqUID", newUUID())
	req.Header.Set("Authorization", "Basic "+c.credentials)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gigachat oauth: http %d", resp.StatusCode)
	}
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresAt   int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("gigachat oauth: %w", err)
	}
	c.token = r.AccessToken
	c.expires = time.UnixMilli(r.ExpiresAt)
	return c.token, nil
}

// Warm fetches the access token ahead of the first reply: the OAuth round trip takes
// seconds, and a player should not wait for it.
func (c *Client) Warm(ctx context.Context) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	// Also resolve and connect to the chat host, so its address is cached too.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.Replace(chatURL, "/chat/completions", "/models", 1), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) Chat(ctx context.Context, messages []Message, maxTokens int) (string, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{
		"model":              c.model,
		"messages":           messages,
		"max_tokens":         maxTokens,
		"temperature":        0.9,
		"repetition_penalty": 1.15, // GigaChat-specific; stops the same phrasing every minute
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			c.mu.Lock()
			c.token = ""
			c.mu.Unlock()
		}
		return "", fmt.Errorf("gigachat chat: http %d", resp.StatusCode)
	}
	var r struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("gigachat chat: %w", err)
	}
	if len(r.Choices) == 0 {
		return "", fmt.Errorf("gigachat chat: no choices")
	}
	return r.Choices[0].Message.Content, nil
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// cachedDialer resolves each host once and dials the remembered IP afterwards. TLS still
// verifies against the request's host name, since the transport sets SNI from the URL.
type cachedDialer struct {
	dialer *net.Dialer
	mu     sync.Mutex
	ips    map[string]string
	path   string // optional file the addresses survive restarts in
}

// PersistDNS keeps resolved GigaChat addresses in a file, so a restart does not wait on a
// resolver that times out. Addresses read from the file are tried first and replaced on failure.
func (c *Client) PersistDNS(path string) {
	d := c.dialer
	d.mu.Lock()
	defer d.mu.Unlock()
	d.path = path
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &d.ips)
		if d.ips == nil {
			d.ips = map[string]string{}
		}
	}
}

func (d *cachedDialer) save() {
	if d.path == "" {
		return
	}
	raw, _ := json.Marshal(d.ips)
	_ = os.WriteFile(d.path, raw, 0o644)
}

func (d *cachedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	ip := d.ips[host]
	d.mu.Unlock()
	if ip != "" {
		if conn, err := d.dialer.DialContext(ctx, network, net.JoinHostPort(ip, port)); err == nil {
			return conn, nil
		}
	}
	conn, err := d.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if remote, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		d.mu.Lock()
		if d.ips[host] != remote.IP.String() {
			d.ips[host] = remote.IP.String()
			d.save()
		}
		d.mu.Unlock()
	}
	return conn, nil
}

// WithModel returns a client for another model that shares the token and connections.
func (c *Client) WithModel(model string) *Client {
	return &Client{dialer: c.dialer, credentials: c.credentials, scope: c.scope, model: model, http: c.http,
		token: c.token, expires: c.expires}
}

// Models lists model ids available to these credentials.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.Replace(chatURL, "/chat/completions", "/models", 1), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range r.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
