package vault

import (
	"testing"
)

func TestParseSecretData_KV2(t *testing.T) {
	body := []byte(`{
		"request_id": "abc",
		"data": {
			"data": {
				"database.password": "fake-db-password-for-test",
				"encryption.key": "fake-encryption-key-32-bytes-aaaa"
			},
			"metadata": {"version": 1, "created_time": "2026-01-01T00:00:00Z"}
		}
	}`)

	result, err := parseSecretData(body, 2)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result["database.password"] != "fake-db-password-for-test" {
		t.Errorf("database.password = %q, want %q", result["database.password"], "fake-db-password-for-test")
	}
	if result["encryption.key"] != "fake-encryption-key-32-bytes-aaaa" {
		t.Errorf("encryption.key = %q, want %q", result["encryption.key"], "fake-encryption-key-32-bytes-aaaa")
	}
	// metadata must NOT leak into result
	if _, ok := result["version"]; ok {
		t.Error("metadata fields leaked into secret data")
	}
}

func TestParseSecretData_KV1(t *testing.T) {
	body := []byte(`{
		"request_id": "abc",
		"data": {
			"salt": "test-salt-value",
			"emailSecret": "fake-email-secret-for-unit-test"
		}
	}`)

	result, err := parseSecretData(body, 1)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result["salt"] != "test-salt-value" {
		t.Errorf("salt = %q, want %q", result["salt"], "test-salt-value")
	}
	if result["emailSecret"] != "fake-email-secret-for-unit-test" {
		t.Errorf("emailSecret = %q, want %q", result["emailSecret"], "fake-email-secret-for-unit-test")
	}
}

func TestParseSecretData_KV2_EmptySecret(t *testing.T) {
	// v2 path that was written with no fields, or soft-deleted:
	// data.data is null, data.metadata still present
	body := []byte(`{
		"data": {
			"data": null,
			"metadata": {"version": 0, "destroyed": true}
		}
	}`)

	_, err := parseSecretData(body, 2)
	if err == nil {
		t.Fatal("expected error for empty/deleted v2 secret, got nil")
	}
}

func TestParseSecretData_KV2_NoDataField(t *testing.T) {
	// Response with no data at all (e.g. wrong path)
	body := []byte(`{"errors": ["no handler for route"]}`)

	_, err := parseSecretData(body, 2)
	if err == nil {
		t.Fatal("expected error for response without data, got nil")
	}
}

func TestParseSecretData_KV1_NoDataField(t *testing.T) {
	body := []byte(`{"errors": ["no handler for route"]}`)

	_, err := parseSecretData(body, 1)
	if err == nil {
		t.Fatal("expected error for response without data, got nil")
	}
}

func TestParseSecretData_InvalidJSON(t *testing.T) {
	body := []byte(`{not valid json`)

	_, err := parseSecretData(body, 2)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestToStringMap(t *testing.T) {
	m := map[string]interface{}{
		"str":   "hello",
		"empty": nil,
		"num":   float64(42),
		"bool":  true,
	}
	result := toStringMap(m)
	if result["str"] != "hello" {
		t.Errorf("str = %q, want %q", result["str"], "hello")
	}
	if result["empty"] != "" {
		t.Errorf("empty = %q, want %q", result["empty"], "")
	}
	if result["num"] != "42" {
		t.Errorf("num = %q, want %q", result["num"], "42")
	}
	if result["bool"] != "true" {
		t.Errorf("bool = %q, want %q", result["bool"], "true")
	}
}
