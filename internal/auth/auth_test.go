package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$600000$") {
		t.Fatalf("unexpected format %q", h)
	}
	if !VerifyPassword(h, "correct horse battery") {
		t.Fatal("right password rejected")
	}
	if VerifyPassword(h, "correct horse batterY") {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
}

func TestPasswordRules(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	for _, bad := range []string{"", "md5$x", "pbkdf2-sha256$1000$c2FsdA$aGFzaA", "pbkdf2-sha256$x$y$z"} {
		if VerifyPassword(bad, "anything") {
			t.Errorf("%q verified", bad)
		}
	}
}

func testUsers(t *testing.T) *Users {
	h, _ := HashPassword("admin password 1")
	v, _ := HashPassword("viewer password 1")
	u, err := NewUsers([]User{
		{Username: "alice", Role: RoleAdmin, PasswordHash: h},
		{Username: "bob", Role: RoleViewer, PasswordHash: v},
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAuthenticate(t *testing.T) {
	u := testUsers(t)
	if user, ok := u.Authenticate("alice", "admin password 1"); !ok || user.Role != RoleAdmin {
		t.Fatal("alice should sign in as admin")
	}
	if _, ok := u.Authenticate("alice", "viewer password 1"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, ok := u.Authenticate("mallory", "admin password 1"); ok {
		t.Fatal("unknown user accepted")
	}
}

func TestUsersValidation(t *testing.T) {
	h, _ := HashPassword("some password 1")
	for name, list := range map[string][]User{
		"empty":     nil,
		"bad name":  {{Username: "Alice!", Role: RoleAdmin, PasswordHash: h}},
		"bad role":  {{Username: "alice", Role: "root", PasswordHash: h}},
		"duplicate": {{Username: "alice", Role: RoleAdmin, PasswordHash: h}, {Username: "alice", Role: RoleViewer, PasswordHash: h}},
	} {
		if _, err := NewUsers(list); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func roundTrip(s *Sessions, user string) *http.Request {
	rec := httptest.NewRecorder()
	s.Issue(rec, user)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestSessionCookie(t *testing.T) {
	s, err := NewSessions(strings.Repeat("k", 32), true)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Issue(rec, "alice")
	c := rec.Result().Cookies()[0]
	if c.Name != CookieName || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie flags: %+v", c)
	}

	if u, ok := s.User(roundTrip(s, "alice")); !ok || u != "alice" {
		t.Fatal("valid cookie rejected")
	}

	// A cookie signed with another secret is rejected.
	other, _ := NewSessions(strings.Repeat("x", 32), true)
	if _, ok := s.User(roundTrip(other, "alice")); ok {
		t.Fatal("forged cookie accepted")
	}

	// Changing the username breaks the signature.
	req := roundTrip(s, "bob")
	ck, _ := req.Cookie(CookieName)
	payload, sig, _ := strings.Cut(ck.Value, ".")
	tampered := httptest.NewRequest(http.MethodGet, "/", nil)
	tampered.AddCookie(&http.Cookie{Name: CookieName, Value: payload[:len(payload)-2] + "xx." + sig})
	if _, ok := s.User(tampered); ok {
		t.Fatal("tampered cookie accepted")
	}

	// Expired cookies are rejected.
	issued := roundTrip(s, "alice")
	s.now = func() time.Time { return time.Now().Add(SessionLifetime + time.Minute) }
	if _, ok := s.User(issued); ok {
		t.Fatal("expired cookie accepted")
	}
}

func TestShortSecretRejected(t *testing.T) {
	if _, err := NewSessions("short", true); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter(3, time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if l.Blocked("ip") {
			t.Fatalf("blocked after %d failures", i)
		}
		l.Fail("ip")
	}
	if !l.Blocked("ip") {
		t.Fatal("should be blocked after 3 failures")
	}
	if l.Blocked("other-ip") {
		t.Fatal("limits are per key")
	}
	now = now.Add(2 * time.Minute)
	if l.Blocked("ip") {
		t.Fatal("failures should expire after the window")
	}
	l.Fail("ip")
	l.Reset("ip")
	if l.Blocked("ip") {
		t.Fatal("reset should clear failures")
	}
}
