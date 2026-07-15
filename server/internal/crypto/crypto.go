// Package crypto holds the four cryptographic primitives the gateway needs:
// API-key generation and hashing, PKCE verification on the client leg, and
// authenticated encryption of the Jira tokens at rest.
//
// The token cipher is AES-256-GCM. The desktop client already generates its
// PKCE challenge with crypto/sha256 and base64url (see internal/sync/providers/
// gateway/connect.go), and the transforms here are the server side of the same
// scheme.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// KeyPrefix is prepended to every API key. A visible prefix makes a leaked key
// greppable in logs and recognisable to secret scanners — the difference between
// a key that gets revoked and one that sits in a paste bin for a year.
const KeyPrefix = "tt_"

// keyBytes is the entropy in a raw API key, before the prefix.
const keyBytes = 32

// prefixVisible is how many characters of the key past the prefix are kept as a
// public, non-secret label so a user can tell two keys apart in a list.
const prefixVisible = 6

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------

// NewAPIKey returns (plaintext, sha256 hash, public prefix). The plaintext is
// shown to the user exactly once; only the hash is ever stored.
func NewAPIKey() (raw, hash, prefix string, err error) {
	buf := make([]byte, keyBytes)
	if _, err = io.ReadFull(rand.Reader, buf); err != nil {
		return "", "", "", fmt.Errorf("generating api key: %w", err)
	}
	raw = KeyPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashAPIKey(raw), raw[:len(KeyPrefix)+prefixVisible], nil
}

// HashAPIKey is a plain SHA-256, deliberately not a password KDF: the input is a
// 256-bit random value, not a human-chosen password. There is no dictionary to
// attack and nothing a work factor would buy but latency on every request.
func HashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// PKCE (RFC 7636), on the client <-> backend leg
// ---------------------------------------------------------------------------

// PKCEChallenge is the S256 transform of a verifier: base64url(sha256(v)),
// unpadded. Identical to the client's challenge() in connect.go.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// PKCEVerify checks a verifier against a stored challenge in constant time.
func PKCEVerify(verifier, challenge string) bool {
	got := PKCEChallenge(verifier)
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// ---------------------------------------------------------------------------
// Encryption at rest
// ---------------------------------------------------------------------------

// Cipher encrypts and decrypts short secrets (the Jira tokens) with AES-256-GCM.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds a Cipher from the configured key. The key is base64 (standard
// or url-safe, padded or not) of exactly 32 bytes.
func NewCipher(key string) (*Cipher, error) {
	raw, err := decodeKey(strings.TrimSpace(key))
	if err != nil {
		return nil, fmt.Errorf(
			"token_encryption_key is not valid: %w. Generate one with "+
				"'task-timer-server gen-key'", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf(
			"token_encryption_key must decode to 32 bytes, got %d. Generate one with "+
				"'task-timer-server gen-key'", len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("token_encryption_key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("token_encryption_key: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals a value and returns base64(nonce || ciphertext || tag).
func (c *Cipher) Encrypt(value string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(value), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
//
// A failure here almost always means the encryption key was rotated or
// regenerated out from under the database. The message says so, rather than
// surfacing a bare "cipher: message authentication failed" that sends someone
// hunting through Atlassian's logs for a fault that is ours.
func (c *Cipher) Decrypt(value string) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("decrypt: stored token is not valid base64: %w", err)
	}
	n := c.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("decrypt: stored token is too short to be valid")
	}
	nonce, ct := sealed[:n], sealed[n:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", errors.New(
			"could not decrypt a stored Jira token: the token_encryption_key does not " +
				"match the one the token was encrypted with. If the key was rotated, " +
				"affected users must reconnect Jira")
	}
	return string(plain), nil
}

// GenerateKey returns a fresh, standard-base64 AES-256 key, ready to drop into
// /etc/task-timer-server/token_encryption_key.
func GenerateKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// decodeKey accepts either base64 alphabet, padded or unpadded.
func decodeKey(key string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if raw, err := enc.DecodeString(key); err == nil {
			return raw, nil
		}
	}
	return nil, errors.New("not base64")
}
