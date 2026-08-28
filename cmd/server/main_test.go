package main

import "testing"

func TestDatabasePasswordSource(t *testing.T) {
	// Note: empty env and unset env are indistinguishable to the source
	// detection (both fall back to config.yaml), so one case covers both.
	cases := []struct {
		name      string
		fromVault bool
		envValue  string
		want      string
	}{
		{"vault wins over env", true, "env-pw", "vault"},
		{"vault wins over empty env", true, "", "vault"},
		{"env wins over yaml", false, "env-pw", "env"},
		{"empty env falls back to yaml", false, "", "config.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VMS_DB_PASSWORD", tc.envValue)
			if got := databasePasswordSource(tc.fromVault); got != tc.want {
				t.Fatalf("databasePasswordSource(%v) = %q, want %q", tc.fromVault, got, tc.want)
			}
		})
	}
}
