package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the built-in defaults do not validate: %v", err)
	}
}

func TestLoadMergesFileOverDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	const body = `
server:
  addr: 0.0.0.0:9000
database:
  name: other_db
  port: 3399
auth:
  session_ttl: 1h
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Addr != "0.0.0.0:9000" {
		t.Errorf("Server.Addr = %q, want the file value", cfg.Server.Addr)
	}
	if cfg.Database.Name != "other_db" || cfg.Database.Port != 3399 {
		t.Errorf("database settings not taken from the file: %+v", cfg.Database)
	}
	if cfg.Auth.SessionTTL != time.Hour {
		t.Errorf("Auth.SessionTTL = %v, want 1h", cfg.Auth.SessionTTL)
	}
	// Untouched keys must keep their defaults rather than zeroing out.
	if cfg.Database.User != "simulation" || cfg.Auth.BcryptCost != 12 {
		t.Errorf("defaults were lost when the file was merged: %+v %+v", cfg.Database, cfg.Auth)
	}
}

// A missing file is normal: defaults plus environment must be enough to start.
func TestLoadToleratesMissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load with no file: %v", err)
	}
	if cfg.Database.Name != "simulation" {
		t.Errorf("expected defaults, got %+v", cfg.Database)
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("database:\n  name: from_file\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv(EnvPrefix+"DB_NAME", "from_env")
	t.Setenv(EnvPrefix+"DB_PASSWORD", "secret")
	t.Setenv(EnvPrefix+"PLUGINS_ENABLED", "true")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Database.Name != "from_env" {
		t.Errorf("Database.Name = %q, want the environment value", cfg.Database.Name)
	}
	if cfg.Database.Password != "secret" {
		t.Errorf("the password did not come from the environment")
	}
	if !cfg.Plugins.Enabled {
		t.Error("a boolean environment override was ignored")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	tests := []struct {
		name  string
		mutfn func(*Config)
		want  string
	}{
		{"no address", func(c *Config) { c.Server.Addr = "" }, "server.addr"},
		{"no database name", func(c *Config) { c.Database.Name = "" }, "database host, name and user"},
		{"port out of range", func(c *Config) { c.Database.Port = 70000 }, "database.port"},
		{"bcrypt cost too low", func(c *Config) { c.Auth.BcryptCost = 4 }, "bcrypt_cost"},
		{"no session lifetime", func(c *Config) { c.Auth.SessionTTL = 0 }, "session_ttl"},
		{"half a bootstrap admin", func(c *Config) { c.Auth.BootstrapAdminEmail = "a@b.c" }, "bootstrap admin"},
		{"short bootstrap password", func(c *Config) {
			c.Auth.BootstrapAdminEmail = "a@b.c"
			c.Auth.BootstrapAdminPassword = "short"
		}, "at least 12 characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutfn(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s", tt.name)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestDSNCarriesTheRequiredParameters(t *testing.T) {
	d := Database{Host: "db.local", Port: 3306, Name: "sim", User: "u", Password: "p"}
	dsn := d.DSN()

	// parseTime and a UTC connection zone are not optional: without them
	// DATETIME columns scan as strings in an unpredictable zone.
	for _, want := range []string{"parseTime=true", "loc=UTC", "charset=utf8mb4", "u:p@tcp(db.local:3306)/sim"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q is missing %q", dsn, want)
		}
	}
}

func TestDSNIsDeterministic(t *testing.T) {
	d := Database{Host: "h", Port: 1, Name: "n", User: "u", Params: map[string]string{"a": "1", "z": "2"}}
	if d.DSN() != d.DSN() {
		t.Fatal("DSN parameter order is unstable between calls")
	}
}

func TestRedactedDSNHidesThePassword(t *testing.T) {
	d := Database{Host: "h", Port: 3306, Name: "n", User: "u", Password: "hunter2"}
	if strings.Contains(d.RedactedDSN(), "hunter2") {
		t.Fatal("RedactedDSN leaked the password")
	}
}

func TestCookieKey(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv(EnvPrefix+"COOKIE_KEY", "")
		_, ok, err := CookieKey()
		if err != nil || ok {
			t.Fatalf("CookieKey with no value = ok %v, err %v; want false, nil", ok, err)
		}
	})

	t.Run("valid", func(t *testing.T) {
		t.Setenv(EnvPrefix+"COOKIE_KEY", strings.Repeat("ab", 32))
		key, ok, err := CookieKey()
		if err != nil || !ok || len(key) != 32 {
			t.Fatalf("CookieKey = %d bytes, ok %v, err %v; want 32 bytes", len(key), ok, err)
		}
	})

	t.Run("wrong length", func(t *testing.T) {
		t.Setenv(EnvPrefix+"COOKIE_KEY", "abcdef")
		if _, _, err := CookieKey(); err == nil {
			t.Fatal("CookieKey accepted a key that is not 32 bytes")
		}
	})

	t.Run("not hex", func(t *testing.T) {
		t.Setenv(EnvPrefix+"COOKIE_KEY", strings.Repeat("zz", 32))
		if _, _, err := CookieKey(); err == nil {
			t.Fatal("CookieKey accepted a non-hex value")
		}
	})
}

func TestNormalizeMakesPathsAbsolute(t *testing.T) {
	cfg := Default()
	cfg.Storage.DataDir = "data"
	cfg.Server.BaseURL = "http://host/"

	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !filepath.IsAbs(cfg.Storage.DataDir) {
		t.Errorf("DataDir %q is not absolute", cfg.Storage.DataDir)
	}
	if cfg.Server.BaseURL != "http://host" {
		t.Errorf("BaseURL = %q, want the trailing slash removed", cfg.Server.BaseURL)
	}
}
