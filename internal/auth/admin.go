package auth

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble"
	"golang.org/x/crypto/bcrypt"
)

// Store provides auth persistence on top of the shared Pebble database.
type Store struct {
	db *pebble.DB
	// cluster carries the replication + single-writer seams. Both zero on a
	// standalone server; see cluster.go.
	cluster clusterHooks
}

func NewStore(db *pebble.DB) *Store {
	return &Store{db: db}
}

// CreateAdmin hashes the password and persists the admin user.
func (s *Store) CreateAdmin(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	user := AdminUser{
		Username:  username,
		PassHash:  hash,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("marshal admin: %w", err)
	}
	// Deliberately ungated: CreateAdmin is part of node-local bootstrap, which
	// runs before any cluster role exists (see Store.SetWriteGate). It is
	// replicated so a failover does not lose the admin record.
	return s.set(adminUserKey(username), data)
}

// ValidateAdmin returns nil if username/password are correct.
func (s *Store) ValidateAdmin(username, password string) error {
	data, closer, err := s.db.Get(adminUserKey(username))
	if err != nil {
		return fmt.Errorf("user not found")
	}
	defer closer.Close()

	var user AdminUser
	if err := json.Unmarshal(data, &user); err != nil {
		return fmt.Errorf("corrupt admin record: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword(user.PassHash, []byte(password)); err != nil {
		return fmt.Errorf("invalid password")
	}
	return nil
}

// ChangeAdminPassword updates the password for an existing admin user.
//
// Changing the password ends every open session: the session token carries a
// fingerprint of the password hash, so old tokens no longer validate.
// Re-setting the same password is a no-op — otherwise a restart with
// MUNINN_ADMIN_PASSWORD set would re-hash the password and log everyone out.
func (s *Store) ChangeAdminPassword(username, newPassword string) error {
	// Cluster single-writer gate (#596). Bootstrap's MUNINN_ADMIN_PASSWORD seed
	// also calls this, but it runs before SetWriteGate is wired, so seeding a
	// node's own admin record at startup is unaffected.
	if err := s.refuseNonLeaderWrite(); err != nil {
		return err
	}
	key := adminUserKey(username)
	val, closer, err := s.db.Get(key)
	if err != nil {
		return fmt.Errorf("admin user not found: %w", err)
	}
	var u AdminUser
	if jsonErr := json.Unmarshal(val, &u); jsonErr != nil {
		closer.Close()
		return jsonErr
	}
	closer.Close()
	if bcrypt.CompareHashAndPassword(u.PassHash, []byte(newPassword)) == nil {
		return nil // same password: keep the hash (and every session) alive
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PassHash = hash
	encoded, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return s.set(key, encoded)
}

// adminFingerprint returns the session fingerprint for an admin user, derived
// from the stored password hash and the session secret.
func (s *Store) adminFingerprint(username string, secret []byte) (string, bool) {
	data, closer, err := s.db.Get(adminUserKey(username))
	if err != nil {
		return "", false
	}
	defer closer.Close()

	var user AdminUser
	if err := json.Unmarshal(data, &user); err != nil {
		return "", false
	}
	return sessionFingerprint(user.PassHash, secret), true
}

// NewSessionToken creates a session token for an admin user, bound to the
// current password hash. Changing the password invalidates the token.
func (s *Store) NewSessionToken(username string, secret []byte) (string, error) {
	fingerprint, ok := s.adminFingerprint(username, secret)
	if !ok {
		return "", fmt.Errorf("admin user not found")
	}
	return NewSessionToken(username, fingerprint, secret)
}

// ValidateSessionToken reports whether the token is a valid, unexpired session
// for an existing admin user whose password has not changed since it was
// issued.
func (s *Store) ValidateSessionToken(token string, secret []byte) bool {
	username, _, _, ok := parseSessionToken(token, secret)
	if !ok {
		return false
	}
	fingerprint, ok := s.adminFingerprint(username, secret)
	if !ok {
		return false
	}
	return validateSessionToken(token, fingerprint, secret)
}

// AdminExists returns true if at least one admin user has been created.
func (s *Store) AdminExists() bool {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte{prefixAdminUser},
		UpperBound: []byte{prefixAdminUser + 1},
	})
	if err != nil {
		return false
	}
	defer iter.Close()
	return iter.First()
}
