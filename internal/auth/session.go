package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CookieName is the session cookie. The __Host- prefix makes the browser
// refuse it unless it is Secure, has Path=/ and no Domain, so a subdomain
// can't set or overwrite it.
const (
	CookieName        = "__Host-console_session"
	InsecureCookie    = "console_session" // for http://localhost only
	SessionLifetime   = 8 * time.Hour
	minSecretLength   = 32
	maxCookieByteSize = 1024
)

// Sessions issues and checks signed, expiring session cookies. They are
// stateless (HMAC-SHA256 over the username and expiry), so they survive
// restarts and work across replicas. See docs/adr/0003.
type Sessions struct {
	secret []byte
	secure bool
	now    func() time.Time
}

type claims struct {
	User    string `json:"u"`
	Expires int64  `json:"e"`
}

// NewSessions needs a secret of at least 32 bytes. secure=false is only for
// local development over plain http.
func NewSessions(secret string, secure bool) (*Sessions, error) {
	if len(secret) < minSecretLength {
		return nil, errors.New("session secret must be at least 32 characters")
	}
	return &Sessions{secret: []byte(secret), secure: secure, now: time.Now}, nil
}

func (s *Sessions) name() string {
	if s.secure {
		return CookieName
	}
	return InsecureCookie
}

func (s *Sessions) sign(payload string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Issue sets a session cookie for username.
func (s *Sessions) Issue(w http.ResponseWriter, username string) {
	exp := s.now().Add(SessionLifetime)
	body, _ := json.Marshal(claims{User: username, Expires: exp.Unix()})
	payload := base64.RawURLEncoding.EncodeToString(body)
	http.SetCookie(w, &http.Cookie{
		Name:     s.name(),
		Value:    payload + "." + s.sign(payload),
		Path:     "/",
		Expires:  exp,
		MaxAge:   int(SessionLifetime.Seconds()),
		HttpOnly: true, // no JavaScript access, so XSS can't steal it
		Secure:   s.secure,
		SameSite: http.SameSiteStrictMode, // not sent on cross-site requests
	})
}

// Clear removes the session cookie.
func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.name(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode,
	})
}

// User returns the username in a valid, unexpired session cookie.
func (s *Sessions) User(r *http.Request) (string, bool) {
	c, err := r.Cookie(s.name())
	if err != nil || len(c.Value) > maxCookieByteSize {
		return "", false
	}
	payload, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return "", false
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	var cl claims
	if json.Unmarshal(body, &cl) != nil || cl.User == "" || s.now().Unix() >= cl.Expires {
		return "", false
	}
	return cl.User, true
}

// LoginLimiter blocks a client after too many failed logins in a window.
// It is per replica and in memory; enough to stop online password guessing.
type LoginLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string][]time.Time
	now      func() time.Time
}

// NewLoginLimiter allows max failures per key per window.
func NewLoginLimiter(max int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{max: max, window: window, failures: map[string][]time.Time{}, now: time.Now}
}

func (l *LoginLimiter) prune(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	kept := l.failures[key][:0]
	for _, t := range l.failures[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
		return nil
	}
	l.failures[key] = kept
	return kept
}

// Blocked reports whether key has used up its failed attempts.
func (l *LoginLimiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) >= l.max
}

// Fail records a failed attempt.
func (l *LoginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures) > 10_000 { // bound memory if many clients fail
		for k := range l.failures {
			l.prune(k)
		}
	}
	l.failures[key] = append(l.prune(key), l.now())
}

// Reset clears a key after a successful login.
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}
