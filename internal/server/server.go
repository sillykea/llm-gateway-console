// Package server is the console's HTTP API and the host for its web UI.
//
// Two listeners, on purpose:
//   - the public one serves the UI and /api to people, behind sign-in;
//   - the internal one serves /internal/keys to gateways, behind a shared
//     token, and is never exposed through the ingress.
//
// Keeping them on separate ports means an ingress misconfiguration can't
// expose the key list, and a leaked sync token is useless from outside.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/sillykea/llm-gateway-console/internal/auth"
	"github.com/sillykea/llm-gateway-console/internal/prom"
	"github.com/sillykea/llm-gateway-console/internal/store"
)

// Metrics is what the API needs from Prometheus. An interface so tests and
// a console without Prometheus can swap it out.
type Metrics interface {
	Overview(ctx context.Context) (*prom.Overview, error)
	SpendByKey(ctx context.Context) (map[string]float64, error)
}

// Server holds the console's dependencies.
type Server struct {
	users     *auth.Users
	sessions  *auth.Sessions
	logins    *auth.LoginLimiter
	store     *store.Store
	metrics   Metrics // nil when no Prometheus is configured
	syncToken []byte
	ui        fs.FS // the built web app; may be empty in development
	log       *slog.Logger
}

// Options configures a Server.
type Options struct {
	Users     *auth.Users
	Sessions  *auth.Sessions
	Store     *store.Store
	Metrics   Metrics
	SyncToken string
	UI        fs.FS
	Logger    *slog.Logger
}

// New validates options and returns a server.
func New(o Options) (*Server, error) {
	if o.Users == nil || o.Sessions == nil || o.Store == nil {
		return nil, errors.New("server: users, sessions and store are required")
	}
	if len(o.SyncToken) < 32 {
		return nil, errors.New("server: the gateway sync token must be at least 32 characters")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Server{
		users: o.Users, sessions: o.Sessions, store: o.Store, metrics: o.Metrics,
		syncToken: []byte(o.SyncToken), ui: o.UI, log: o.Logger,
		logins: auth.NewLoginLimiter(5, 15*time.Minute),
	}, nil
}

// PublicHandler serves the UI and the API for people.
func (s *Server) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.require(auth.RoleViewer, s.handleMe))
	mux.HandleFunc("GET /api/overview", s.require(auth.RoleViewer, s.handleOverview))
	mux.HandleFunc("GET /api/keys", s.require(auth.RoleViewer, s.handleListKeys))
	mux.HandleFunc("POST /api/keys", s.require(auth.RoleAdmin, s.handleCreateKey))
	mux.HandleFunc("PATCH /api/keys/{id}", s.require(auth.RoleAdmin, s.handleUpdateKey))
	mux.HandleFunc("POST /api/keys/{id}/revoke", s.require(auth.RoleAdmin, s.handleRevokeKey))
	mux.HandleFunc("GET /api/audit", s.require(auth.RoleAdmin, s.handleAudit))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	mux.Handle("/", s.uiHandler())

	return s.withRecover(s.withLog(withSecurityHeaders(withSameOrigin(mux))))
}

// InternalHandler serves the key list to gateways.
func (s *Server) InternalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /internal/keys", s.handleSyncKeys)
	return s.withRecover(mux)
}

// --- middleware ---

type ctxKey struct{}

func userFrom(r *http.Request) auth.User {
	u, _ := r.Context().Value(ctxKey{}).(auth.User)
	return u
}

// require checks the session and the user's role. Admins can do everything
// viewers can. The user is looked up on every request, so a role change in
// the users file applies at once (after a restart), not when cookies expire.
func (s *Server) require(role auth.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, ok := s.sessions.User(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		user, ok := s.users.Get(name)
		if !ok {
			s.sessions.Clear(w)
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if role == auth.RoleAdmin && user.Role != auth.RoleAdmin {
			writeError(w, http.StatusForbidden, "admins only")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, user)))
	}
}

// withSameOrigin rejects state-changing requests that come from another
// site. SameSite=Strict cookies already stop most CSRF; this is the second
// layer, using the Origin and Sec-Fetch-Site headers browsers always send
// on cross-site requests. Requiring a JSON body also means a plain HTML
// form can't post here.
func withSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			writeError(w, http.StatusForbidden, "cross-site request refused")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				writeError(w, http.StatusForbidden, "cross-origin request refused")
				return
			}
		}
		if ct := r.Header.Get("Content-Type"); r.ContentLength != 0 && !strings.HasPrefix(ct, "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "send JSON")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withSecurityHeaders sets a strict CSP: the app loads only its own script
// and styles, no inline script, and can't be framed.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *Server) withLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" || !strings.HasPrefix(r.URL.Path, "/api/") {
			return
		}
		s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic", "value", fmt.Sprint(v), "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decode reads a small JSON body and rejects unknown fields, so a typo in
// a field name is an error instead of a silent no-op.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) audit(e store.Event) {
	if err := s.store.Audit(e); err != nil {
		// The change itself already succeeded; losing the audit line is
		// bad but shouldn't undo it. Log loudly so it gets noticed.
		s.log.Error("audit write failed", "error", err, "action", e.Action)
	}
}

// --- handlers ---

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	// Limit by client and by username, so neither guessing many passwords
	// for one account nor one password across many accounts gets far.
	byIP, byUser := "ip:"+clientIP(r), "user:"+in.Username
	if s.logins.Blocked(byIP) || s.logins.Blocked(byUser) {
		writeError(w, http.StatusTooManyRequests, "too many failed sign-ins, try again in 15 minutes")
		return
	}
	user, ok := s.users.Authenticate(in.Username, in.Password)
	if !ok {
		s.logins.Fail(byIP)
		s.logins.Fail(byUser)
		s.audit(store.Event{Actor: in.Username, Action: "login.failed", Detail: clientIP(r)})
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	s.logins.Reset(byUser)
	s.sessions.Issue(w, user.Username)
	s.audit(store.Event{Actor: user.Username, Action: "login", Detail: clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]string{"username": user.Username, "role": string(user.Role)})
}

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	s.sessions.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username, "role": string(u.Role)})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics are not configured (PROMETHEUS_URL)")
		return
	}
	o, err := s.metrics.Overview(r.Context())
	if err != nil {
		s.log.Warn("overview failed", "error", err)
		writeError(w, http.StatusBadGateway, "couldn't read metrics from Prometheus")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// keyView is a key as the UI sees it: the record plus this month's spend.
// The hash is left out; nobody needs it in a browser.
type keyView struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	Prefix           string     `json:"prefix"`
	RPM              int        `json:"rpm"`
	MonthlyBudgetUSD float64    `json:"monthly_budget_usd"`
	SpendMonthUSD    *float64   `json:"spend_month_usd"` // null when metrics are unavailable
	CreatedAt        time.Time  `json:"created_at"`
	CreatedBy        string     `json:"created_by"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevokedBy        string     `json:"revoked_by,omitempty"`
}

func toView(k store.Key, spend map[string]float64) keyView {
	v := keyView{
		ID: k.ID, Name: k.Name, Description: k.Description, Prefix: k.Prefix,
		RPM: k.RPM, MonthlyBudgetUSD: k.MonthlyBudgetUSD,
		CreatedAt: k.CreatedAt, CreatedBy: k.CreatedBy, RevokedAt: k.RevokedAt, RevokedBy: k.RevokedBy,
	}
	if spend != nil {
		x := spend[k.Name]
		v.SpendMonthUSD = &x
	}
	return v
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	var spend map[string]float64
	if s.metrics != nil {
		// Best effort: the key list must still load when Prometheus is down.
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if m, err := s.metrics.SpendByKey(ctx); err == nil {
			spend = m
		} else {
			s.log.Warn("spend by key failed", "error", err)
		}
	}
	keys := s.store.List()
	out := make([]keyView, len(keys))
	for i, k := range keys {
		out[i] = toView(k, spend)
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out, "metrics_available": spend != nil})
}

func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such key")
	default:
		writeError(w, http.StatusInternalServerError, "couldn't save the change")
	}
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var in store.NewKey
	if !decode(w, r, &in) {
		return
	}
	actor := userFrom(r).Username
	k, secret, err := s.store.Create(actor, in)
	if err != nil {
		storeError(w, err)
		return
	}
	s.audit(store.Event{Actor: actor, Action: "key.create", Target: k.Name,
		Detail: fmt.Sprintf("rpm=%d budget=%.2f", k.RPM, k.MonthlyBudgetUSD)})
	// The only time the secret ever leaves the console.
	writeJSON(w, http.StatusCreated, map[string]any{"key": toView(k, nil), "secret": secret})
}

func (s *Server) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	var in store.Update
	if !decode(w, r, &in) {
		return
	}
	actor := userFrom(r).Username
	k, err := s.store.UpdateKey(r.PathValue("id"), in)
	if err != nil {
		storeError(w, err)
		return
	}
	s.audit(store.Event{Actor: actor, Action: "key.update", Target: k.Name,
		Detail: fmt.Sprintf("rpm=%d budget=%.2f", k.RPM, k.MonthlyBudgetUSD)})
	writeJSON(w, http.StatusOK, toView(k, nil))
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r).Username
	k, err := s.store.Revoke(actor, r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	s.audit(store.Event{Actor: actor, Action: "key.revoke", Target: k.Name})
	writeJSON(w, http.StatusOK, toView(k, nil))
}

func (s *Server) handleAudit(w http.ResponseWriter, _ *http.Request) {
	events, err := s.store.RecentEvents(200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "couldn't read the audit log")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleSyncKeys serves active keys, as hashes, to gateways. The ETag is the
// store version, so an unchanged list costs one comparison and a 304.
func (s *Server) handleSyncKeys(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	// Compare hashes so the comparison is constant-time whatever the length.
	got, want := sha256.Sum256([]byte(token)), sha256.Sum256(s.syncToken)
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid sync token")
		return
	}
	keys, version := s.store.ActiveKeys()
	etag := fmt.Sprintf(`"v%d"`, version)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	type entry struct {
		Name             string  `json:"name"`
		SHA256           string  `json:"sha256"`
		RPM              int     `json:"rpm"`
		MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
	}
	out := make([]entry, 0, len(keys))
	for _, k := range keys {
		if _, err := hex.DecodeString(k.SHA256); err != nil {
			continue // can't happen for keys this console made; never send junk
		}
		out = append(out, entry{Name: k.Name, SHA256: k.SHA256, RPM: k.RPM, MonthlyBudgetUSD: k.MonthlyBudgetUSD})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// uiHandler serves the built single-page app. Hashed asset files are cached
// for a year; index.html is never cached, so a deploy shows up on the next
// load. Unknown paths get index.html, so links into the app work.
func (s *Server) uiHandler() http.Handler {
	if s.ui == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "the web UI isn't built: run `make web`", http.StatusServiceUnavailable)
		})
	}
	files := http.FileServerFS(s.ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" && p != "index.html" {
			if f, err := s.ui.Open(p); err == nil {
				f.Close()
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(p, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		index, err := fs.ReadFile(s.ui, "index.html")
		if err != nil {
			http.Error(w, "the web UI isn't built: run `make web`", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}
