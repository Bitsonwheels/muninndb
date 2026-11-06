package auth

import "testing"

// A session token is bound to the admin's current password hash. Changing the
// password must end every existing session (audit finding 4) - otherwise a
// stolen cookie stays valid for up to 24 hours after the owner rotates the
// password.

func TestSessionToken_InvalidatedByPasswordChange(t *testing.T) {
	store := newBypassTestStore(t)
	secret := []byte("session-secret")

	if err := store.CreateAdmin("admin", "password"); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	token, err := store.NewSessionToken("admin", secret)
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if !store.ValidateSessionToken(token, secret) {
		t.Fatal("a fresh token must validate")
	}

	if err := store.ChangeAdminPassword("admin", "new-password"); err != nil {
		t.Fatalf("ChangeAdminPassword: %v", err)
	}

	if store.ValidateSessionToken(token, secret) {
		t.Error("the old token must not validate after the password changed")
	}

	fresh, err := store.NewSessionToken("admin", secret)
	if err != nil {
		t.Fatalf("NewSessionToken after change: %v", err)
	}
	if !store.ValidateSessionToken(fresh, secret) {
		t.Error("a token issued after the password change must validate")
	}
}

// Re-applying the same password (MUNINN_ADMIN_PASSWORD on every start) must not
// re-hash the password and must not log every open session out.
func TestChangeAdminPassword_SamePasswordKeepsSession(t *testing.T) {
	store := newBypassTestStore(t)
	secret := []byte("session-secret")

	if err := store.CreateAdmin("admin", "password"); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	token, err := store.NewSessionToken("admin", secret)
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}

	if err := store.ChangeAdminPassword("admin", "password"); err != nil {
		t.Fatalf("ChangeAdminPassword: %v", err)
	}

	if !store.ValidateSessionToken(token, secret) {
		t.Error("re-applying the same password must not invalidate the session")
	}
}
