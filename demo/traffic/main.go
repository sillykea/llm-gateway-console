// Command traffic drives the demo: it signs in to the console as an admin,
// creates an API key per team through the console's API (the same way a
// person would in the UI), then sends a steady mix of requests through the
// gateway with those keys, so the console has real spend and traffic to show.
//
// The mix is chosen to exercise the gateway:
//   - repeated questions at temperature 0, which the cache answers;
//   - questions containing an email address, which stay on the local model;
//   - a team with a small budget, which runs out after a few minutes.
//
// Keys are only shown once, so they are kept in STATE_FILE; restarting the
// generator reuses them instead of creating new ones.
//
//	CONSOLE_URL      e.g. http://console:8090
//	GATEWAY_URL      e.g. http://gateway:8080
//	ADMIN_USER       console admin username
//	ADMIN_PASSWORD   console admin password
//	STATE_FILE       where keys are kept (default /state/keys.json)
//	RPS              requests per second (default 3)
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type team struct {
	Name   string
	Desc   string
	RPM    int
	Budget float64
	Weight float64
}

// The teams the demo creates. batch-jobs has a small budget on purpose.
var teams = []team{
	{"team-search", "Search answers", 300, 25, 0.5},
	{"support-bot", "Customer support assistant", 120, 10, 0.35},
	{"batch-jobs", "Nightly summarisation", 600, 0.25, 0.15},
}

var faq = []string{
	"How do I reset my password?",
	"What are your opening hours?",
	"How long does shipping to Auckland take?",
	"Can I change my delivery address after ordering?",
	"Do you ship to Australia?",
}

var topics = []string{
	"Summarise this support ticket about a late delivery",
	"Write a product description for a waterproof jacket",
	"Explain our refund policy in plain English",
	"Draft a reply to a customer asking about stock",
	"Classify this review as positive or negative",
	"Suggest three subject lines for a newsletter",
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

type client struct {
	console string
	gateway string
	http    *http.Client
}

func (c *client) post(url string, body any, out any) (int, error) {
	b, _ := json.Marshal(body)
	resp, err := c.http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

func waitFor(url string) {
	for {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		log.Printf("waiting for %s", url)
		time.Sleep(2 * time.Second)
	}
}

func loadState(path string) map[string]string {
	keys := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &keys)
	}
	return keys
}

func saveState(path string, keys map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(keys, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

// ensureKeys creates any team key the state file doesn't have yet.
func (c *client) ensureKeys(stateFile string) (map[string]string, error) {
	keys := loadState(stateFile)
	for _, t := range teams {
		if keys[t.Name] != "" {
			continue
		}
		var out struct {
			Secret string `json:"secret"`
		}
		status, err := c.post(c.console+"/api/keys", map[string]any{
			"name": t.Name, "description": t.Desc, "rpm": t.RPM, "monthly_budget_usd": t.Budget,
		}, &out)
		switch {
		case err != nil:
			return nil, err
		case status == http.StatusConflict:
			// The console has the key but we lost its secret (state volume
			// removed on its own). Carry on with the other teams.
			log.Printf("key %s exists but its secret isn't in %s; skipping it (run `make demo-reset` to start fresh)", t.Name, stateFile)
			continue
		case status != http.StatusCreated:
			return nil, fmt.Errorf("creating key %s: status %d", t.Name, status)
		}
		keys[t.Name] = out.Secret
		log.Printf("created key %s", t.Name)
		if err := saveState(stateFile, keys); err != nil {
			return nil, err
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("no usable keys")
	}
	return keys, nil
}

func keyWorks(gateway, key string) bool {
	req, _ := http.NewRequest(http.MethodGet, gateway+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func pickTeam(keys map[string]string) (string, string) {
	r := rand.Float64()
	for _, t := range teams {
		if r < t.Weight && keys[t.Name] != "" {
			return t.Name, keys[t.Name]
		}
		r -= t.Weight
	}
	for name, k := range keys {
		return name, k
	}
	return "", ""
}

// nextRequest builds one realistic request body.
func nextRequest(n int64) map[string]any {
	model := "chat-default"
	if rand.Float64() < 0.1 {
		model = "chat-private"
	}
	switch r := rand.Float64(); {
	case r < 0.25: // a common question: cacheable
		return map[string]any{"model": model, "temperature": 0,
			"messages": []map[string]string{{"role": "user", "content": faq[rand.IntN(len(faq))]}}}
	case r < 0.35: // contains personal data: the gateway keeps it on the local model
		return map[string]any{"model": model, "temperature": 0.7,
			"messages": []map[string]string{{"role": "user", "content": fmt.Sprintf("Customer jane.doe%d@example.com asked about order %d. Draft a reply.", n%50, 10000+n)}}}
	default:
		return map[string]any{"model": model, "temperature": 0.7,
			"messages": []map[string]string{{"role": "user", "content": fmt.Sprintf("%s (#%d)", topics[rand.IntN(len(topics))], n)}}}
	}
}

func main() {
	jar, _ := cookiejar.New(nil)
	c := &client{
		console: env("CONSOLE_URL", "http://console:8090"),
		gateway: env("GATEWAY_URL", "http://gateway:8080"),
		http:    &http.Client{Jar: jar, Timeout: 30 * time.Second},
	}
	rps, err := strconv.ParseFloat(env("RPS", "3"), 64)
	if err != nil || rps <= 0 {
		log.Fatal("RPS must be a positive number")
	}

	waitFor(c.console + "/healthz")
	if status, err := c.post(c.console+"/api/login", map[string]string{
		"username": env("ADMIN_USER", "admin"), "password": os.Getenv("ADMIN_PASSWORD"),
	}, nil); err != nil || status != http.StatusOK {
		log.Fatalf("console sign-in failed: status %d, %v", status, err)
	}
	keys, err := c.ensureKeys(env("STATE_FILE", "/state/keys.json"))
	if err != nil {
		log.Fatal(err)
	}

	// The gateway picks new keys up on its next sync, so wait until one works.
	waitFor(c.gateway + "/readyz")
	for _, k := range keys {
		for !keyWorks(c.gateway, k) {
			log.Printf("waiting for the gateway to sync the new keys")
			time.Sleep(time.Second)
		}
		break
	}
	log.Printf("sending about %.1f requests per second through %s", rps, c.gateway)

	var (
		n      atomic.Int64
		mu     sync.Mutex
		counts = map[string]int{}
	)
	gw := &http.Client{Timeout: 60 * time.Second}
	go func() {
		for range time.Tick(15 * time.Second) {
			mu.Lock()
			log.Printf("last 15s: %v", counts)
			counts = map[string]int{}
			mu.Unlock()
		}
	}()

	ticker := time.NewTicker(time.Duration(float64(time.Second) / rps))
	for range ticker.C {
		go func() {
			i := n.Add(1)
			_, key := pickTeam(keys)
			body, _ := json.Marshal(nextRequest(i))
			req, _ := http.NewRequest(http.MethodPost, c.gateway+"/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			label := "error"
			if resp, err := gw.Do(req); err == nil {
				resp.Body.Close()
				label = strconv.Itoa(resp.StatusCode)
				if resp.Header.Get("X-Cache") == "HIT" {
					label = "200 (cache)"
				}
			}
			mu.Lock()
			counts[label]++
			mu.Unlock()
		}()
	}
}
