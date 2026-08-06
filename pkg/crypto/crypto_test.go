package crypto

import (
	"os"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	os.Setenv("VMS_ENCRYPTION_KEY", "test-key-32-bytes-long-123456789")
	if err := Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tests := []string{
		"admin123",
		"",
		"password with spaces",
		"中文密码测试",
		"!@#$%^&*()_+{}|:\"<>?",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 51 bytes
	}

	for _, plaintext := range tests {
		enc, err := Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", plaintext, err)
		}
		if enc == "" {
			t.Fatalf("Encrypt(%q): empty result", plaintext)
		}

		dec, err := Decrypt(enc)
		if err != nil {
			t.Fatalf("Decrypt(%q): %v", plaintext, err)
		}
		if dec != plaintext {
			t.Fatalf("roundtrip failed: got %q, want %q", dec, plaintext)
		}
	}
}

func TestEncryptWithoutInit(t *testing.T) {
	encryptionKey = nil
	_, err := Encrypt("test")
	if err != ErrKeyNotSet {
		t.Fatalf("expected ErrKeyNotSet, got %v", err)
	}
}

func TestDecryptWithoutInit(t *testing.T) {
	encryptionKey = nil
	_, err := Decrypt("dGVzdA==")
	if err != ErrKeyNotSet {
		t.Fatalf("expected ErrKeyNotSet, got %v", err)
	}
}

func TestDecryptCorruptedCiphertext(t *testing.T) {
	os.Setenv("VMS_ENCRYPTION_KEY", "test-key-32-bytes-long-123456789")
	if err := Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Valid base64 but not a valid GCM ciphertext
	_, err := Decrypt("dGVzdA==")
	if err != ErrDecryptShort {
		t.Fatalf("expected ErrDecryptShort, got %v", err)
	}

	// Long enough but wrong key / random data
	_, err = Decrypt("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != ErrDecryptFail {
		t.Fatalf("expected ErrDecryptFail, got %v", err)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	os.Setenv("VMS_ENCRYPTION_KEY", "key-A-32-bytes-long-key-12345678")
	if err := Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	enc, err := Encrypt("hello")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Switch to a different key
	encryptionKey = []byte("key-B-32-bytes-long-key-12345678")

	_, err = Decrypt(enc)
	if err != ErrDecryptFail {
		t.Fatalf("expected ErrDecryptFail with wrong key, got %v", err)
	}
}

func TestInitKeyTooShort(t *testing.T) {
	os.Setenv("VMS_ENCRYPTION_KEY", "short")
	err := Init()
	if err == nil {
		t.Fatal("expected error for short key, got nil")
	}
}

func TestInitKeyNotSet(t *testing.T) {
	os.Unsetenv("VMS_ENCRYPTION_KEY")
	err := Init()
	if err != ErrKeyNotSet {
		t.Fatalf("expected ErrKeyNotSet, got %v", err)
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	os.Setenv("VMS_ENCRYPTION_KEY", "test-key-32-bytes-long-123456789")
	if err := Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	e1, _ := Encrypt("hello")
	e2, _ := Encrypt("hello")
	if e1 == e2 {
		t.Fatal("same plaintext should produce different ciphertexts due to random nonce")
	}
}
