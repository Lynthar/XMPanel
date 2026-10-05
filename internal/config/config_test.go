package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A config file that never mentions rate limiting keeps it on; only an
// explicit `enabled: false` turns the per-IP limiter off.
func TestRateLimitStaysOnUnlessDisabled(t *testing.T) {
	const base = "database:\n  encryption_key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"
	for name, tc := range map[string]struct {
		yaml string
		want bool
	}{
		"omitted":  {base, true},
		"other":    {base + "security:\n  rate_limit:\n    burst: 10\n", true},
		"disabled": {base + "security:\n  rate_limit:\n    enabled: false\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XMPANEL_CONFIG", path)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Security.RateLimit.Enabled != tc.want {
				t.Errorf("enabled = %v, want %v", cfg.Security.RateLimit.Enabled, tc.want)
			}
		})
	}
	if !DefaultConfig().Security.RateLimit.Enabled {
		t.Error("DefaultConfig leaves rate limiting off")
	}
}
