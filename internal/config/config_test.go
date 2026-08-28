package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempConfig writes a minimal valid config.yaml into a temp dir and
// returns its path. password is injected as the yaml value.
func writeTempConfig(t *testing.T, password string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	content := `server:
  port: 8081
database:
  host: mysql
  port: 3306
  user: root
  password: "` + password + `"
  dbname: swnms
mediamtx:
  url: "http://aiovms-mediamtx:9997"
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

// VMS_DB_PASSWORD overrides database.password in config.yaml.
func TestLoad_PasswordFromEnv(t *testing.T) {
	p := writeTempConfig(t, "yaml-pw")
	t.Setenv("VMS_DB_PASSWORD", "env-pw")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Database.Password; got != "env-pw" {
		t.Fatalf("database.password = %q, want env value %q", got, "env-pw")
	}
}

// An explicitly-set-but-empty VMS_DB_PASSWORD is ignored by viper
// (allowEmptyEnv=false), so database.password falls back to config.yaml.
func TestLoad_EmptyEnvIgnoredFallsBackToYAML(t *testing.T) {
	p := writeTempConfig(t, "yaml-pw")
	t.Setenv("VMS_DB_PASSWORD", "")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Database.Password; got != "yaml-pw" {
		t.Fatalf("database.password = %q, want yaml fallback %q", got, "yaml-pw")
	}
}

// Without VMS_DB_PASSWORD set at all, config.yaml is the source.
func TestLoad_PasswordFromYAML(t *testing.T) {
	p := writeTempConfig(t, "yaml-pw")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Database.Password; got != "yaml-pw" {
		t.Fatalf("database.password = %q, want %q", got, "yaml-pw")
	}
}
