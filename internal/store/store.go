// Package store keeps API key records and the audit log on disk.
//
// The console holds only a SHA-256 hash of each key; the key itself is shown
// once, when it is created. Data is a JSON file written atomically (write a
// temp file, fsync, rename, fsync the directory), so a crash mid-write
// leaves the old file intact. That is enough for one replica and a few
// thousand keys; docs/adr/0004 covers why, and when to move to Postgres.
package store

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Key is an API key record. It never holds the key itself.
type Key struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Description      string     `json:"description,omitempty"`
	Prefix           string     `json:"prefix"` // first characters, so people can tell keys apart
	SHA256           string     `json:"sha256"`
	RPM              int        `json:"rpm"`
	MonthlyBudgetUSD float64    `json:"monthly_budget_usd"`
	CreatedAt        time.Time  `json:"created_at"`
	CreatedBy        string     `json:"created_by"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevokedBy        string     `json:"revoked_by,omitempty"`
}

// Active reports whether the key can still be used.
func (k Key) Active() bool { return k.RevokedAt == nil }

// Event is one line of the audit log.
type Event struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"` // key.create, key.update, key.revoke, login, login.failed
	Target string    `json:"target,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Errors the API maps to status codes.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid")
)

// Limits on what a key can be given.
const (
	MaxRPM    = 100_000
	MaxBudget = 1_000_000
	maxDesc   = 200
	keyPrefix = "lgw_"
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,39}$`)

type file struct {
	Version int64 `json:"version"`
	Keys    []Key `json:"keys"`
}

// Store is safe for concurrent use.
type Store struct {
	dir string
	now func() time.Time

	mu      sync.RWMutex
	version int64
	keys    []Key

	auditMu sync.Mutex
}

// Open loads (or creates) the store in dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, now: time.Now}
	data, err := os.ReadFile(s.path())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path(), err)
	}
	s.version, s.keys = f.Version, f.Keys
	return s, nil
}

func (s *Store) path() string      { return filepath.Join(s.dir, "keys.json") }
func (s *Store) auditPath() string { return filepath.Join(s.dir, "audit.jsonl") }

// Version changes on every write. The key list's ETag is built from it.
func (s *Store) Version() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// List returns every key, newest first.
func (s *Store) List() []Key {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Key(nil), s.keys...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ActiveKeys returns keys that haven't been revoked, and the version they
// were read at, in one consistent snapshot.
func (s *Store) ActiveKeys() ([]Key, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Key
	for _, k := range s.keys {
		if k.Active() {
			out = append(out, k)
		}
	}
	return out, s.version
}

// NewKey is what an admin fills in to create a key.
type NewKey struct {
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	RPM              int     `json:"rpm"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
}

func validLimits(rpm int, budget float64) error {
	if rpm < 0 || rpm > MaxRPM {
		return fmt.Errorf("%w: rpm must be between 0 and %d", ErrInvalid, MaxRPM)
	}
	if budget < 0 || budget > MaxBudget || budget != budget { // budget != budget catches NaN
		return fmt.Errorf("%w: monthly budget must be between 0 and %d", ErrInvalid, MaxBudget)
	}
	return nil
}

// Create makes a key and returns its record and the secret. The secret is
// not stored and can't be shown again.
func (s *Store) Create(actor string, in NewKey) (Key, string, error) {
	if !nameRE.MatchString(in.Name) {
		return Key{}, "", fmt.Errorf("%w: name must be 2-40 lowercase letters, digits or dashes", ErrInvalid)
	}
	if len(in.Description) > maxDesc {
		return Key{}, "", fmt.Errorf("%w: description is too long", ErrInvalid)
	}
	if err := validLimits(in.RPM, in.MonthlyBudgetUSD); err != nil {
		return Key{}, "", err
	}

	raw := make([]byte, 32) // 256 bits: the gateway relies on this (its ADR 6)
	if _, err := rand.Read(raw); err != nil {
		return Key{}, "", err
	}
	secret := keyPrefix + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))
	id := make([]byte, 8)
	rand.Read(id)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		// Names are never reused, even after revocation: the gateway tracks
		// spend by name, so a reused name would inherit the old key's spend.
		if k.Name == in.Name {
			return Key{}, "", fmt.Errorf("%w: a key named %q already exists", ErrConflict, in.Name)
		}
	}
	k := Key{
		ID: hex.EncodeToString(id), Name: in.Name, Description: in.Description,
		Prefix: secret[:len(keyPrefix)+6], SHA256: hex.EncodeToString(sum[:]),
		RPM: in.RPM, MonthlyBudgetUSD: in.MonthlyBudgetUSD,
		CreatedAt: s.now().UTC(), CreatedBy: actor,
	}
	if err := s.save(append(append([]Key(nil), s.keys...), k)); err != nil {
		return Key{}, "", err
	}
	return k, secret, nil
}

// Update is the set of fields an admin can change on an existing key.
type Update struct {
	Description      *string  `json:"description"`
	RPM              *int     `json:"rpm"`
	MonthlyBudgetUSD *float64 `json:"monthly_budget_usd"`
}

// UpdateKey changes a key's limits or description.
func (s *Store) UpdateKey(id string, u Update) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.find(id)
	if i < 0 {
		return Key{}, ErrNotFound
	}
	k := s.keys[i]
	if !k.Active() {
		return Key{}, fmt.Errorf("%w: key is revoked", ErrConflict)
	}
	if u.Description != nil {
		if len(*u.Description) > maxDesc {
			return Key{}, fmt.Errorf("%w: description is too long", ErrInvalid)
		}
		k.Description = *u.Description
	}
	if u.RPM != nil {
		k.RPM = *u.RPM
	}
	if u.MonthlyBudgetUSD != nil {
		k.MonthlyBudgetUSD = *u.MonthlyBudgetUSD
	}
	if err := validLimits(k.RPM, k.MonthlyBudgetUSD); err != nil {
		return Key{}, err
	}
	next := append([]Key(nil), s.keys...)
	next[i] = k
	if err := s.save(next); err != nil {
		return Key{}, err
	}
	return k, nil
}

// Revoke stops a key working. It stays listed for the record.
func (s *Store) Revoke(actor, id string) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.find(id)
	if i < 0 {
		return Key{}, ErrNotFound
	}
	k := s.keys[i]
	if !k.Active() {
		return Key{}, fmt.Errorf("%w: key is already revoked", ErrConflict)
	}
	now := s.now().UTC()
	k.RevokedAt, k.RevokedBy = &now, actor
	next := append([]Key(nil), s.keys...)
	next[i] = k
	if err := s.save(next); err != nil {
		return Key{}, err
	}
	return k, nil
}

func (s *Store) find(id string) int {
	for i, k := range s.keys {
		if k.ID == id {
			return i
		}
	}
	return -1
}

// save writes keys atomically, then makes them current. Caller holds mu.
// If the write fails, nothing in memory changes.
func (s *Store) save(keys []Key) error {
	data, err := json.MarshalIndent(file{Version: s.version + 1, Keys: keys}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "keys-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path()); err != nil {
		return err
	}
	if d, err := os.Open(s.dir); err == nil { // persist the rename itself
		d.Sync()
		d.Close()
	}
	s.version++
	s.keys = keys
	return nil
}

// Audit appends an event to the audit log and syncs it to disk.
func (s *Store) Audit(e Event) error {
	if e.At.IsZero() {
		e.At = s.now().UTC()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	f, err := os.OpenFile(s.auditPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// RecentEvents returns up to limit events, newest first.
func (s *Store) RecentEvents(limit int) ([]Event, error) {
	if limit <= 0 {
		return []Event{}, nil
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	f, err := os.Open(s.auditPath())
	if errors.Is(err, os.ErrNotExist) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Keep only the last `limit` events in a ring buffer, so memory stays
	// bounded however long the log gets.
	ring := make([]Event, limit)
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue // a torn last line after a crash: skip it
		}
		ring[n%limit] = e
		n++
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	count := min(n, limit)
	out := make([]Event, count)
	for i := range count {
		out[i] = ring[(n-1-i)%limit]
	}
	return out, nil
}
