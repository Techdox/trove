package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Severity levels for a notification.
const (
	LevelInfo     = "info"
	LevelWarning  = "warning"
	LevelCritical = "critical"
	LevelResolved = "resolved"
)

// Notification is one outbound message, channel-agnostic.
type Notification struct {
	Kind    string    `json:"kind"`  // state | health | agent | host | freshness | test
	Level   string    `json:"level"` // info | warning | critical | resolved
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	Host    string    `json:"host,omitempty"`
	Service string    `json:"service,omitempty"`
	Agent   string    `json:"agent,omitempty"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	At      time.Time `json:"at"`
}

// Dispatcher delivers a notification to one channel.
type Dispatcher interface {
	Name() string
	Send(ctx context.Context, n Notification) error
}

// Dispatchers builds the set of configured channel dispatchers.
func Dispatchers(cfg Config) []Dispatcher {
	client := &http.Client{Timeout: 10 * time.Second}
	var out []Dispatcher
	if cfg.WebhookURL != "" {
		out = append(out, &webhookDispatcher{client: client, url: cfg.WebhookURL, secret: cfg.WebhookSecret})
	}
	if cfg.DiscordURL != "" {
		out = append(out, &discordDispatcher{client: client, url: cfg.DiscordURL})
	}
	if cfg.NtfyURL != "" {
		out = append(out, &ntfyDispatcher{client: client, url: cfg.NtfyURL, token: cfg.NtfyToken})
	}
	return out
}

// post sends a request with small retries; bodies are rebuilt per attempt.
func post(ctx context.Context, client *http.Client, build func() (*http.Request, error)) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		req, err := build()
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		// Client errors won't improve on retry.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
			return lastErr
		}
	}
	return lastErr
}

// ---- generic webhook -------------------------------------------------------

type webhookDispatcher struct {
	client *http.Client
	url    string
	secret string
}

func (d *webhookDispatcher) Name() string { return "webhook" }

func (d *webhookDispatcher) Send(ctx context.Context, n Notification) error {
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return post(ctx, d.client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "trove-alert")
		if d.secret != "" {
			timestamp := strconv.FormatInt(n.At.UTC().Unix(), 10)
			req.Header.Set("X-Trove-Timestamp", timestamp)
			req.Header.Set("X-Trove-Signature", signWebhookPayload(d.secret, timestamp, payload))
		}
		return req, nil
	})
}

func signWebhookPayload(secret, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ---- Discord ---------------------------------------------------------------

type discordDispatcher struct {
	client *http.Client
	url    string
}

func (d *discordDispatcher) Name() string { return "discord" }

// Catppuccin-ish embed colors per level.
var discordColors = map[string]int{
	LevelInfo:     0x89b4fa, // blue
	LevelWarning:  0xf9e2af, // yellow
	LevelCritical: 0xf38ba8, // red
	LevelResolved: 0xa6e3a1, // green
}

func (d *discordDispatcher) Send(ctx context.Context, n Notification) error {
	embed := map[string]any{
		"title":       n.Title,
		"description": n.Body,
		"color":       discordColors[n.Level],
		"timestamp":   n.At.UTC().Format(time.RFC3339),
		"footer":      map[string]any{"text": "trove"},
	}
	payload, err := json.Marshal(map[string]any{"embeds": []any{embed}})
	if err != nil {
		return err
	}
	return post(ctx, d.client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

// ---- ntfy ------------------------------------------------------------------

type ntfyDispatcher struct {
	client *http.Client
	url    string
	token  string
}

func (d *ntfyDispatcher) Name() string { return "ntfy" }

var ntfyPriority = map[string]string{
	LevelInfo:     "default",
	LevelWarning:  "high",
	LevelCritical: "urgent",
	LevelResolved: "default",
}

var ntfyTags = map[string]string{
	LevelInfo:     "information_source",
	LevelWarning:  "warning",
	LevelCritical: "rotating_light",
	LevelResolved: "white_check_mark",
}

func (d *ntfyDispatcher) Send(ctx context.Context, n Notification) error {
	return post(ctx, d.client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, strings.NewReader(n.Body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Title", n.Title)
		req.Header.Set("Priority", ntfyPriority[n.Level])
		req.Header.Set("Tags", ntfyTags[n.Level])
		if d.token != "" {
			req.Header.Set("Authorization", "Bearer "+d.token)
		}
		return req, nil
	})
}
