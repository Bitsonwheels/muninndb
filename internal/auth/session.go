package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

const sessionTTL = 24 * time.Hour

// fingerprintLen is the length of the password fingerprint in the session
// payload. 16 base64url characters = 96 bits of a keyed hash.
const fingerprintLen = 16

// NewSessionToken creates a signed session token for an admin user.
// Format: base64url(username + "|" + expiry_unix + "|" + fingerprint) + "." + base64url(HMAC)
//
// The fingerprint binds the token to the admin's current password hash (see
// sessionFingerprint), so changing the password invalidates every issued token.
func NewSessionToken(username, fingerprint string, secret []byte) (string, error) {
	expiry := time.Now().Add(sessionTTL).Unix()
	payload := username + "|" + strconv.FormatInt(expiry, 10) + "|" + fingerprint
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	sig := computeHMAC(encoded, secret)
	return encoded + "." + sig, nil
}

// parseSessionToken verifies the HMAC and decodes the payload.
// Returns the username, the password fingerprint and the expiry.
func parseSessionToken(token string, secret []byte) (username, fingerprint string, expiry int64, ok bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", "", 0, false
	}
	encoded, sig := parts[0], parts[1]
	computed := computeHMAC(encoded, secret)
	if !hmac.Equal([]byte(computed), []byte(sig)) {
		return "", "", 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", 0, false
	}
	username, rest, ok := strings.Cut(string(payload), "|")
	if !ok || username == "" {
		return "", "", 0, false
	}
	expiryStr, fingerprint, ok := strings.Cut(rest, "|")
	if !ok {
		return "", "", 0, false
	}
	expiry, err = strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return "", "", 0, false
	}
	return username, fingerprint, expiry, true
}

// validateSessionToken verifies the HMAC, the expiry and the password
// fingerprint. Returns true if valid.
func validateSessionToken(token, fingerprint string, secret []byte) bool {
	_, tokenFingerprint, expiry, ok := parseSessionToken(token, secret)
	if !ok {
		return false
	}
	if time.Now().Unix() >= expiry {
		return false
	}
	return hmac.Equal([]byte(tokenFingerprint), []byte(fingerprint))
}

// sessionFingerprint derives a short, keyed fingerprint from the admin's
// password hash. The hash itself never leaves the store, and the fingerprint is
// useless without the session secret.
func sessionFingerprint(passHash []byte, secret []byte) string {
	return computeHMAC(string(passHash), secret)[:fingerprintLen]
}

func computeHMAC(data string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// GenerateSecret creates a 32-byte random secret for session signing.
// Call once at startup and persist to the data directory.
func GenerateSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
