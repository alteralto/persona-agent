// Package laya talks to a local laya-serve instance (POST /v1/systemone).
package laya

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type Answer struct {
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type Client struct {
	url  string
	http *http.Client
}

func New(baseURL string) *Client {
	return &Client{url: baseURL + "/v1/systemone", http: &http.Client{Timeout: 3 * time.Second}}
}

func (c *Client) Decide(ctx context.Context, state string, questions map[string]Question) (map[string]Answer, error) {
	body, _ := json.Marshal(map[string]any{
		"state":     state,
		"model":     "multilingual",
		"questions": questions,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("laya: http %d", resp.StatusCode)
	}
	var r struct {
		Answers map[string]Answer `json:"answers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("laya: %w", err)
	}
	return r.Answers, nil
}
