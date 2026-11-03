package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestComplete(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer K" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"  Ringkasan.  "}}]}`))
	}))
	defer srv.Close()
	out, err := Complete(context.Background(), nil, Request{Endpoint: srv.URL + "/v1/", Key: "K", Model: "m", System: "S", User: "U", MaxTokens: 50, Timeout: 5 * time.Second})
	if err != nil || out != "Ringkasan." {
		t.Fatalf("%q %v", out, err)
	}
	if got["stream"] != false || got["model"] != "m" {
		t.Fatalf("request: %v", got)
	}
	if _, err := Complete(context.Background(), nil, Request{Endpoint: srv.URL + "/v1", Key: "salah", Model: "m", Timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("auth error not surfaced: %v", err)
	}
}

func TestValidEndpoint(t *testing.T) {
	for v, want := range map[string]bool{
		"https://api.openai.com/v1": true, "http://10.0.1.1:8080/v1": true, "http://127.0.0.1:11434/v1": true, "http://localhost:8080/v1": true,
		"http://example.com/v1": false, "ftp://x": false, "https://u:p@x/v1": false, "https://x/v1?k=1": false, "http://8.8.8.8/v1": false,
	} {
		if ValidEndpoint(v) != want {
			t.Errorf("%s: want %v", v, want)
		}
	}
}
