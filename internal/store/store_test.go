package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestCreateStoresOnlyTheHash(t *testing.T) {
	s, dir := open(t)
	k, secret, err := s.Create("alice", NewKey{Name: "team-a", RPM: 60, MonthlyBudgetUSD: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "lgw_") || len(secret) != 4+43 {
		t.Fatalf("secret %q: want lgw_ + 43 base64url chars", secret)
	}
	sum := sha256.Sum256([]byte(secret))
	if k.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("stored hash doesn't match the secret")
	}
	if !strings.HasPrefix(secret, k.Prefix) || len(k.Prefix) != 10 {
		t.Fatalf("prefix %q", k.Prefix)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "keys.json"))
	if strings.Contains(string(data), secret) {
		t.Fatal("the secret must never be written to disk")
	}
	info, _ := os.Stat(filepath.Join(dir, "keys.json"))
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("keys.json is readable by others: %v", info.Mode())
	}
}

func TestPersistsAcrossRestarts(t *testing.T) {
	s, dir := open(t)
	k, _, _ := s.Create("alice", NewKey{Name: "team-a"})
	s.Revoke("alice", k.ID)
	v := s.Version()

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Version() != v {
		t.Fatalf("version %d after reopen, want %d", s2.Version(), v)
	}
	got := s2.List()
	if len(got) != 1 || got[0].Active() || got[0].RevokedBy != "alice" {
		t.Fatalf("after reopen: %+v", got)
	}
}

func TestValidation(t *testing.T) {
	s, _ := open(t)
	for name, in := range map[string]NewKey{
		"uppercase":  {Name: "Team"},
		"too short":  {Name: "a"},
		"spaces":     {Name: "team a"},
		"rpm":        {Name: "ok-name", RPM: -1},
		"rpm high":   {Name: "ok-name", RPM: MaxRPM + 1},
		"budget":     {Name: "ok-name", MonthlyBudgetUSD: -5},
		"budget nan": {Name: "ok-name", MonthlyBudgetUSD: math.NaN()},
		"desc":       {Name: "ok-name", Description: strings.Repeat("x", 201)},
	} {
		if _, _, err := s.Create("alice", in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if s.Version() != 0 {
		t.Fatal("rejected input must not write anything")
	}
}

func TestNamesAreNeverReused(t *testing.T) {
	s, _ := open(t)
	k, _, _ := s.Create("alice", NewKey{Name: "team-a"})
	s.Revoke("alice", k.ID)
	if _, _, err := s.Create("alice", NewKey{Name: "team-a"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("reusing a revoked key's name: err = %v", err)
	}
}

func TestUpdateAndRevoke(t *testing.T) {
	s, _ := open(t)
	k, _, _ := s.Create("alice", NewKey{Name: "team-a", RPM: 10})

	rpm, desc := 99, "batch jobs"
	got, err := s.UpdateKey(k.ID, Update{RPM: &rpm, Description: &desc})
	if err != nil || got.RPM != 99 || got.Description != "batch jobs" || got.MonthlyBudgetUSD != 0 {
		t.Fatalf("update: %+v %v", got, err)
	}
	bad := -1
	if _, err := s.UpdateKey(k.ID, Update{RPM: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid update: %v", err)
	}
	if s.List()[0].RPM != 99 {
		t.Fatal("a rejected update changed the key")
	}

	if _, err := s.Revoke("bob", k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke("bob", k.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("double revoke: %v", err)
	}
	if _, err := s.UpdateKey(k.ID, Update{RPM: &rpm}); !errors.Is(err, ErrConflict) {
		t.Fatalf("updating a revoked key: %v", err)
	}
	if _, err := s.Revoke("bob", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke missing: %v", err)
	}
	if active, _ := s.ActiveKeys(); len(active) != 0 {
		t.Fatal("revoked key still active")
	}
}

func TestVersionBumpsOnEveryWrite(t *testing.T) {
	s, _ := open(t)
	k, _, _ := s.Create("a", NewKey{Name: "k-1"})
	v1 := s.Version()
	rpm := 5
	s.UpdateKey(k.ID, Update{RPM: &rpm})
	if s.Version() != v1+1 {
		t.Fatal("update should bump the version")
	}
}

func TestConcurrentCreates(t *testing.T) {
	s, dir := open(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Create("a", NewKey{Name: fmt.Sprintf("key-%d", i)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s2, _ := Open(dir)
	if len(s2.List()) != 20 {
		t.Fatalf("%d keys on disk, want 20", len(s2.List()))
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "keys-*.json"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func TestAuditLog(t *testing.T) {
	s, dir := open(t)
	for i := range 5 {
		if err := s.Audit(Event{Actor: "alice", Action: "key.create", Target: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a crash mid-write: a torn final line is skipped.
	f, _ := os.OpenFile(filepath.Join(dir, "audit.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"actor":"ali`)
	f.Close()

	got, err := s.RecentEvents(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Target != "4" || got[2].Target != "2" {
		t.Fatalf("recent events: %+v", got)
	}
	if got[0].At.IsZero() {
		t.Fatal("events should be timestamped")
	}
	all, _ := s.RecentEvents(100)
	if len(all) != 5 {
		t.Fatalf("%d events, want 5", len(all))
	}
}

func TestAuditEmpty(t *testing.T) {
	s, _ := open(t)
	got, err := s.RecentEvents(10)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}
