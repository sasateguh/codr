// Package ai calls a chat-completions endpoint that speaks the OpenAI wire format (OpenAI, a gateway such as
// 9router or LiteLLM, Ollama, vLLM, …) — the endpoint, model and key are the owner's choice (D71). One
// non-streaming request per call, with a deadline; nothing else leaves the server.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Request is one question to the model.
type Request struct {
	Endpoint  string // base URL ending before /chat/completions, e.g. https://api.openai.com/v1
	Key       string // bearer token, may be empty for a local model
	Model     string
	System    string
	User      string
	MaxTokens int
	Timeout   time.Duration
}

// ValidEndpoint accepts https anywhere, and http only for this machine or a private network (a gateway on the
// same host); no credentials or query in the URL.
func ValidEndpoint(v string) bool {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// Complete returns the model's answer text.
func Complete(ctx context.Context, hc *http.Client, r Request) (string, error) {
	if !ValidEndpoint(r.Endpoint) || r.Model == "" {
		return "", errors.New("ai: endpoint or model missing")
	}
	body, _ := json.Marshal(map[string]any{
		"model": r.Model, "stream": false, "max_tokens": r.MaxTokens, "temperature": 0.2,
		"messages": []map[string]string{{"role": "system", "content": r.System}, {"role": "user", "content": r.User}},
	})
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.Endpoint, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Key != "" {
		req.Header.Set("Authorization", "Bearer "+r.Key)
	}
	if hc == nil {
		hc = &http.Client{}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("ai: HTTP %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", errors.New("ai: the endpoint did not answer in the chat-completions format")
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return "", errors.New("ai: empty answer")
	}
	return text, nil
}
