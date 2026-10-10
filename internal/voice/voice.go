// Package voice connects the agent to the local voice service (voice/service.py):
// recognized player speech comes in on /voice, lines to speak go out to /say.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type Heard struct {
	Nick  string `json:"nick"`
	Text  string `json:"text"`
	STTms int    `json:"stt_ms"`
}

// Listen serves POST /voice on addr and forwards each phrase to out.
func Listen(ctx context.Context, addr string, out chan<- Heard) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /voice", func(w http.ResponseWriter, r *http.Request) {
		var h Heard
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil || h.Nick == "" || h.Text == "" {
			http.Error(w, "bad phrase", http.StatusBadRequest)
			return
		}
		select {
		case out <- h:
		default: // agent busy; a dropped phrase is better than a stalled game thread
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

type Speaker struct {
	url  string
	http *http.Client
}

func NewSpeaker(url string) *Speaker {
	return &Speaker{url: url, http: &http.Client{Timeout: 2 * time.Second}}
}

// Say queues text for speech; the voice service plays it asynchronously.
func (s *Speaker) Say(ctx context.Context, text string) error {
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Mood sets the avatar's face for a while (voice service /mood): "happy", "scared", "grumpy";
// it goes back to neutral by itself. Fire and forget: a missing avatar is no error.
func (s *Speaker) Mood(ctx context.Context, mood string, seconds int) {
	body, _ := json.Marshal(map[string]any{"mood": mood, "seconds": seconds})
	url := strings.TrimSuffix(s.url, "/say") + "/mood"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := s.http.Do(req); err == nil {
		resp.Body.Close()
	}
}
