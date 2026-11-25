package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sillykea/llm-gateway-console/internal/auth"
	"github.com/sillykea/llm-gateway-console/internal/prom"
	"github.com/sillykea/llm-gateway-console/internal/store"
)

const syncToken = "sync-token-sync-token-sync-token-1234"

type fakeMetrics struct{ fail bool }

func (f *fakeMetrics) Overview(context.Context) (*prom.Overview, error) {
	if f.fail {
		return nil, errors.New("down")
	}
	return &prom.Overview{SpendMonthUSD: 42, Requests: 10}, nil
}

func (f *fakeMetrics) SpendByKey(context.Context) (map[string]float64, error) {
	if f.fail {
		return nil, errors.New("down")
	}
	return map[string]float64{"team-a": 3.5}, nil
}

// One users table for the whole package: hashing at 600k iterations is slow.
var testUsers = func() *auth.Users {
	a, _ := auth.HashPassword("admin password 1")
	v, _ := auth.HashPassword("viewer password 1")
	u, err := auth.NewUsers([]auth.User{
		{Username: "alice", Role: auth.RoleAdmin, PasswordHash: a},
		{Username: "bob", Role: auth.RoleViewer, PasswordHash: v},
	})
	if err != nil {
		panic(err)
	}
	return u
}()

type harness struct {
	t        *testing.T
	public   http.Handler
	internal http.Handler
	store    *store.Store
	metrics  *fakeMetrics
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := auth.NewSessions(strings.Repeat("s", 32), true)
	m := &fakeMetrics{}
	ui := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>console</title>")},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
	}
	srv, err := New(Options{
		Users: testUsers, Sessions: sess, Store: st, Metrics: m, SyncToken: syncToken, UI: ui,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, public: srv.PublicHandler(), internal: srv.InternalHandler(), store: st, metrics: m}
}

type resp struct {
	*httptest.ResponseRecorder
}

func (r resp) json(v any) {
	json.Unmarshal(r.Body.Bytes(), v)
}

func (h *harness) do(method, path string, body any, cookies []*http.Cookie, headers ...string) resp {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.public.ServeHTTP(rec, req)
	return resp{rec}
}

func (h *harness) login(user, pass string) []*http.Cookie {
	h.t.Helper()
	r := h.do("POST", "/api/login", map[string]string{"username": user, "password": pass}, nil)
	if r.Code != http.StatusOK {
		h.t.Fatalf("login %s: %d %s", user, r.Code, r.Body)
	}
	return r.Result().Cookies()
}

func TestLoginAndRoles(t *testing.T) {
	h := newHarness(t)
	if r := h.do("GET", "/api/keys", nil, nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", r.Code)
	}

	admin := h.login("alice", "admin password 1")
	viewer := h.login("bob", "viewer password 1")

	var me map[string]string
	h.do("GET", "/api/me", nil, viewer).json(&me)
	if me["username"] != "bob" || me["role"] != "viewer" {
		t.Fatalf("me = %v", me)
	}

	// Viewers can look but not change anything or read the audit log.
	if r := h.do("GET", "/api/keys", nil, viewer); r.Code != http.StatusOK {
		t.Fatalf("viewer list: %d", r.Code)
	}
	if r := h.do("POST", "/api/keys", map[string]any{"name": "team-a"}, viewer); r.Code != http.StatusForbidden {
		t.Fatalf("viewer create: %d", r.Code)
	}
	if r := h.do("GET", "/api/audit", nil, viewer); r.Code != http.StatusForbidden {
		t.Fatalf("viewer audit: %d", r.Code)
	}
	if r := h.do("POST", "/api/keys", map[string]any{"name": "team-a"}, admin); r.Code != http.StatusCreated {
		t.Fatalf("admin create: %d %s", r.Code, r.Body)
	}
}

func TestWrongPasswordAndLockout(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		r := h.do("POST", "/api/login", map[string]string{"username": "alice", "password": "nope nope nope"}, nil)
		if r.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, r.Code)
		}
	}
	r := h.do("POST", "/api/login", map[string]string{"username": "alice", "password": "admin password 1"}, nil)
	if r.Code != http.StatusTooManyRequests {
		t.Fatalf("after 5 failures even the right password should wait: %d", r.Code)
	}
	events, _ := h.store.RecentEvents(10)
	if len(events) != 5 || events[0].Action != "login.failed" {
		t.Fatalf("failed logins should be audited: %+v", events)
	}
}

func TestKeyLifecycleAndSync(t *testing.T) {
	h := newHarness(t)
	admin := h.login("alice", "admin password 1")

	var created struct {
		Key    keyView `json:"key"`
		Secret string  `json:"secret"`
	}
	r := h.do("POST", "/api/keys", map[string]any{"name": "team-a", "rpm": 60, "monthly_budget_usd": 25}, admin)
	r.json(&created)
	if !strings.HasPrefix(created.Secret, "lgw_") || created.Key.Name != "team-a" {
		t.Fatalf("create: %s", r.Body)
	}

	// The list never contains the secret or the hash, and has this month's spend.
	r = h.do("GET", "/api/keys", nil, admin)
	if strings.Contains(r.Body.String(), created.Secret) || strings.Contains(r.Body.String(), "sha256") {
		t.Fatal("key list leaks the secret or the hash")
	}
	var list struct {
		Keys             []keyView `json:"keys"`
		MetricsAvailable bool      `json:"metrics_available"`
	}
	r.json(&list)
	if len(list.Keys) != 1 || list.Keys[0].SpendMonthUSD == nil || *list.Keys[0].SpendMonthUSD != 3.5 || !list.MetricsAvailable {
		t.Fatalf("list: %s", r.Body)
	}

	// The gateway sees the key as a hash.
	sync := func(etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/internal/keys", nil)
		req.Header.Set("Authorization", "Bearer "+syncToken)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		h.internal.ServeHTTP(rec, req)
		return rec
	}
	rec := sync("")
	var synced struct {
		Keys []struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
			RPM    int    `json:"rpm"`
		} `json:"keys"`
	}
	json.Unmarshal(rec.Body.Bytes(), &synced)
	if rec.Code != 200 || len(synced.Keys) != 1 || synced.Keys[0].RPM != 60 || len(synced.Keys[0].SHA256) != 64 {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body)
	}
	etag := rec.Header().Get("ETag")
	if sync(etag).Code != http.StatusNotModified {
		t.Fatal("unchanged list should be a 304")
	}

	// Update, then revoke: each changes the ETag; revoked keys drop out.
	r = h.do("PATCH", "/api/keys/"+created.Key.ID, map[string]any{"rpm": 120}, admin)
	if r.Code != 200 {
		t.Fatalf("update: %d %s", r.Code, r.Body)
	}
	if sync(etag).Code != 200 {
		t.Fatal("ETag should change after an update")
	}
	if r := h.do("POST", "/api/keys/"+created.Key.ID+"/revoke", nil, admin); r.Code != 200 {
		t.Fatalf("revoke: %d %s", r.Code, r.Body)
	}
	json.Unmarshal(sync("").Body.Bytes(), &synced)
	if len(synced.Keys) != 0 {
		t.Fatal("revoked key still synced")
	}

	// Every change is in the audit log, newest first.
	var audit struct {
		Events []store.Event `json:"events"`
	}
	h.do("GET", "/api/audit", nil, admin).json(&audit)
	var actions []string
	for _, e := range audit.Events {
		actions = append(actions, e.Action)
	}
	if strings.Join(actions, ",") != "key.revoke,key.update,key.create,login" {
		t.Fatalf("audit = %v", actions)
	}
}

func TestSyncNeedsToken(t *testing.T) {
	h := newHarness(t)
	for _, tok := range []string{"", "wrong", syncToken + "x"} {
		req := httptest.NewRequest("GET", "/internal/keys", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.internal.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: %d", tok, rec.Code)
		}
	}
	// And the sync endpoint is not on the public listener at all.
	req := httptest.NewRequest("GET", "/internal/keys", nil)
	req.Header.Set("Authorization", "Bearer "+syncToken)
	rec := httptest.NewRecorder()
	h.public.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "sha256") {
		t.Fatal("key hashes must not be served on the public listener")
	}
}

func TestValidationErrors(t *testing.T) {
	h := newHarness(t)
	admin := h.login("alice", "admin password 1")
	cases := []struct {
		body any
		want int
	}{
		{map[string]any{"name": "Bad Name"}, http.StatusBadRequest},
		{map[string]any{"name": "ok-name", "rpm": -1}, http.StatusBadRequest},
		{map[string]any{"name": "ok-name", "unknown_field": 1}, http.StatusBadRequest},
	}
	for _, c := range cases {
		if r := h.do("POST", "/api/keys", c.body, admin); r.Code != c.want {
			t.Errorf("%v: %d %s", c.body, r.Code, r.Body)
		}
	}
	h.do("POST", "/api/keys", map[string]any{"name": "dup"}, admin)
	if r := h.do("POST", "/api/keys", map[string]any{"name": "dup"}, admin); r.Code != http.StatusConflict {
		t.Errorf("duplicate: %d", r.Code)
	}
	if r := h.do("POST", "/api/keys/nope/revoke", nil, admin); r.Code != http.StatusNotFound {
		t.Errorf("missing key: %d", r.Code)
	}
}

func TestCrossSiteRequestsRefused(t *testing.T) {
	h := newHarness(t)
	admin := h.login("alice", "admin password 1")
	body := map[string]any{"name": "team-x"}

	if r := h.do("POST", "/api/keys", body, admin, "Sec-Fetch-Site", "cross-site"); r.Code != http.StatusForbidden {
		t.Errorf("cross-site: %d", r.Code)
	}
	if r := h.do("POST", "/api/keys", body, admin, "Origin", "https://evil.example"); r.Code != http.StatusForbidden {
		t.Errorf("foreign origin: %d", r.Code)
	}
	if r := h.do("POST", "/api/keys", body, admin, "Origin", "http://example.com", "Sec-Fetch-Site", "same-origin"); r.Code != http.StatusCreated {
		t.Errorf("same origin should work: %d %s", r.Code, r.Body)
	}

	// A plain HTML form post (not JSON) is refused.
	req := httptest.NewRequest("POST", "/api/keys", strings.NewReader("name=team-y"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range admin {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.public.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", rec.Code)
	}
}

func TestOverviewAndDegradedMetrics(t *testing.T) {
	h := newHarness(t)
	viewer := h.login("bob", "viewer password 1")
	var o prom.Overview
	r := h.do("GET", "/api/overview", nil, viewer)
	r.json(&o)
	if r.Code != 200 || o.SpendMonthUSD != 42 {
		t.Fatalf("overview: %d %s", r.Code, r.Body)
	}

	h.metrics.fail = true
	if r := h.do("GET", "/api/overview", nil, viewer); r.Code != http.StatusBadGateway {
		t.Fatalf("overview with Prometheus down: %d", r.Code)
	}
	// Keys still load without metrics, with spend marked unknown.
	var list struct {
		MetricsAvailable bool `json:"metrics_available"`
	}
	r = h.do("GET", "/api/keys", nil, viewer)
	r.json(&list)
	if r.Code != 200 || list.MetricsAvailable {
		t.Fatalf("keys with Prometheus down: %d %s", r.Code, r.Body)
	}
}

func TestSecurityHeadersAndUI(t *testing.T) {
	h := newHarness(t)
	r := h.do("GET", "/keys", nil, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "<title>console</title>") {
		t.Fatalf("SPA route should serve index.html: %d", r.Code)
	}
	if !strings.Contains(r.Header().Get("Content-Security-Policy"), "script-src 'self'") ||
		r.Header().Get("X-Content-Type-Options") != "nosniff" || r.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers: %v", r.Header())
	}
	r = h.do("GET", "/assets/app-abc123.js", nil, nil)
	if r.Code != 200 || !strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", r.Code, r.Header())
	}
	if r := h.do("GET", "/assets/missing.js", nil, nil); r.Code != http.StatusNotFound {
		t.Fatalf("missing asset should 404, not serve index.html: %d", r.Code)
	}
	if r := h.do("GET", "/api/nope", nil, nil); r.Code != http.StatusNotFound || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unknown api: %d", r.Code)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	h := newHarness(t)
	h.login("alice", "admin password 1")
	r := h.do("POST", "/api/logout", nil, nil)
	if r.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", r.Code)
	}
	c := r.Result().Cookies()
	if len(c) != 1 || c[0].MaxAge >= 0 {
		t.Fatalf("logout should expire the cookie: %+v", c)
	}
}

func TestNewValidates(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	sess, _ := auth.NewSessions(strings.Repeat("s", 32), true)
	if _, err := New(Options{Users: testUsers, Sessions: sess, Store: st, SyncToken: "short"}); err == nil {
		t.Fatal("short sync token accepted")
	}
	if _, err := New(Options{Sessions: sess, Store: st, SyncToken: syncToken}); err == nil {
		t.Fatal("missing users accepted")
	}
}
