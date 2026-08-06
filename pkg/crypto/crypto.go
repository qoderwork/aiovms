// Package crypto provides AES-256-GCM encryption/decryption for camera passwords.
// The encryption key is read from the VMS_ENCRYPTION_KEY environment variable at
// startup and must be exactly 32 bytes (AES-256). Ciphertext is base64-encoded
// for safe storage in the MySQL cameras.password_enc column.

package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
)

var (
	// encryptionKey is the 32-byte AES-256 key, loaded once at startup.
	encryptionKey []byte

	ErrKeyNotSet    = errors.New("encryption key not set")
	ErrKeyTooShort  = errors.New("encryption key must be at least 32 bytes")
	ErrDecryptShort = errors.New("ciphertext too short")
	ErrDecryptFail  = errors.New("decryption failed: wrong key or corrupted data")
)

// Init loads and validates the encryption key from the environment variable
// VMS_ENCRYPTION_KEY. Must be called once at startup before any Encrypt/Decrypt.
func Init() error {
	key := os.Getenv("VMS_ENCRYPTION_KEY")
	if key == "" {
		return ErrKeyNotSet
	}
	if len(key) < 32 {
		return fmt.Errorf("%w (got %d bytes)", ErrKeyTooShort, len(key))
	}
	// Use exactly first 32 bytes for AES-256
	encryptionKey = []byte(key[:32])
	return nil
}

// Encrypt encrypts plaintext using AES-256-GCM with a random 12-byte nonce.
// Returns a base64-encoded string: nonce(12 bytes) + ciphertext + auth tag.
func Encrypt(plaintext string) (string, error) {
	if encryptionKey == nil {
		return "", ErrKeyNotSet
	}

	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// GCM seals with nonce as prefix
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt reverses Encrypt. Accepts a base64-encoded ciphertext and returns
// the original plaintext.
func Decrypt(encoded string) (string, error) {
	if encryptionKey == nil {
		return "", ErrKeyNotSet
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", ErrDecryptShort
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", ErrDecryptFail
	}

	return string(plaintext), nil
}
