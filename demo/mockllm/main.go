// Command mockllm is a small OpenAI-compatible chat API for the demo. It
// returns canned answers with realistic token counts and latency, and fails a
// configurable share of requests so the gateway's fallback has work to do.
//
//	LISTEN       address (default :8000)
//	NAME         shown in answers, e.g. "cloud" or "local"
//	LATENCY_MS   base latency per request (default 300)
//	FAIL_RATE    share of requests answered with 503, 0 to 1 (default 0)
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envFloat(name string, def float64) float64 {
	f, err := strconv.ParseFloat(os.Getenv(name), 64)
	if err != nil {
		return def
	}
	return f
}

// tokens roughly estimates tokens as 4 characters each, the usual rule of thumb.
func tokens(s string) int { return max(1, len(s)/4) }

type server struct {
	name     string
	latency  time.Duration
	failRate float64
}

func (s *server) answer(req request) string {
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Content
	}
	if len(last) > 60 {
		last = last[:60] + "..."
	}
	return fmt.Sprintf("[%s %s] Here is a short answer to %q. In a real deployment this text comes from the model.", s.name, req.Model, last)
}

func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"invalid JSON"}}`, http.StatusBadRequest)
		return
	}
	// Latency varies between 0.5x and 2x the base, like a real API.
	time.Sleep(time.Duration(float64(s.latency) * (0.5 + 1.5*rand.Float64())))
	if rand.Float64() < s.failRate {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"message":"upstream overloaded (simulated)"}}`))
		return
	}

	text := s.answer(req)
	prompt := 0
	for _, m := range req.Messages {
		prompt += tokens(m.Content)
	}
	u := usage{PromptTokens: prompt, CompletionTokens: tokens(text) + rand.IntN(80)}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	id := fmt.Sprintf("mock-%d", rand.Int64())
	now := time.Now().Unix()

	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": id, "object": "chat.completion", "created": now, "model": req.Model,
			"choices": []any{map[string]any{"index": 0, "message": message{Role: "assistant", Content: text}, "finish_reason": "stop"}},
			"usage":   u,
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	for _, word := range strings.SplitAfter(text, " ") {
		send(map[string]any{"id": id, "object": "chat.completion.chunk", "created": now, "model": req.Model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": word}}}})
		time.Sleep(15 * time.Millisecond)
	}
	send(map[string]any{"id": id, "object": "chat.completion.chunk", "created": now, "model": req.Model, "choices": []any{}, "usage": u})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func main() {
	s := &server{
		name:     env("NAME", "mock"),
		latency:  time.Duration(envFloat("LATENCY_MS", 300)) * time.Millisecond,
		failRate: envFloat("FAIL_RATE", 0),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.chat)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	addr := env("LISTEN", ":8000")
	log.Printf("mock LLM %q on %s (latency %s, fail rate %.0f%%)", s.name, addr, s.latency, s.failRate*100)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
