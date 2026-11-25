package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// Role decides what a user can do. Viewers see everything except the audit
// log; only admins change keys.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

// User is one entry in the users file.
type User struct {
	Username     string `json:"username"`
	Role         Role   `json:"role"`
	PasswordHash string `json:"password_hash"`
}

// Users is the set of people who can sign in. It is read from a file that
// would come from a Kubernetes Secret; see docs/adr/0002 for why there is no
// user management UI.
type Users struct {
	byName map[string]User
}

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)

// LoadUsers reads and validates a users file.
func LoadUsers(path string) (*Users, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read users file: %w", err)
	}
	var list []User
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse users file: %w", err)
	}
	return NewUsers(list)
}

// NewUsers validates a list of users.
func NewUsers(list []User) (*Users, error) {
	u := &Users{byName: map[string]User{}}
	for _, user := range list {
		if !usernameRE.MatchString(user.Username) {
			return nil, fmt.Errorf("users: invalid username %q", user.Username)
		}
		if user.Role != RoleAdmin && user.Role != RoleViewer {
			return nil, fmt.Errorf("users: %q has unknown role %q", user.Username, user.Role)
		}
		if _, dup := u.byName[user.Username]; dup {
			return nil, fmt.Errorf("users: duplicate username %q", user.Username)
		}
		u.byName[user.Username] = user
	}
	if len(u.byName) == 0 {
		return nil, fmt.Errorf("users: the file has no users")
	}
	return u, nil
}

// Authenticate checks a username and password. It does the same amount of
// work whether or not the user exists.
func (u *Users) Authenticate(username, password string) (User, bool) {
	user, ok := u.byName[username]
	hash := user.PasswordHash
	if !ok {
		hash = dummyHash
	}
	if !VerifyPassword(hash, password) || !ok {
		return User{}, false
	}
	return user, true
}

// Get returns a user by name. Sessions look the user up on every request, so
// removing someone from the file (or changing their role) takes effect on
// the next restart without waiting for their cookie to expire.
func (u *Users) Get(username string) (User, bool) {
	user, ok := u.byName[username]
	return user, ok
}
