// Package config loads server configuration from a YAML file with environment
// overrides. Secrets (database password, cookie key) are expected to come from
// the environment on production installs so they never sit in a file IIS serves.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// EnvPrefix is prepended to every environment override, e.g. SIM_DB_PASSWORD.
const EnvPrefix = "SIM_"

type Config struct {
	Server   Server   `yaml:"server"`
	Database Database `yaml:"database"`
	Storage  Storage  `yaml:"storage"`
	Auth     Auth     `yaml:"auth"`
	Engine   Engine   `yaml:"engine"`
	Plugins  Plugins  `yaml:"plugins"`
	Report   Report   `yaml:"report"`
	Log      Log      `yaml:"log"`
}

type Server struct {
	// Addr is the local listen address. IIS reverse-proxies to it, so the
	// default binds to loopback only.
	Addr string `yaml:"addr"`
	// BaseURL is the externally visible origin, used to build absolute links
	// and to let the PDF exporter reach the print routes.
	BaseURL string `yaml:"base_url"`
	// WebRoot is the directory holding the built SPA. Empty means the binary
	// serves its embedded fallback page instead.
	WebRoot         string        `yaml:"web_root"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	// TrustProxyHeaders makes the server read X-Forwarded-For and
	// X-Forwarded-Proto. Only enable it when a proxy you control (IIS ARR)
	// sits in front, otherwise clients can spoof their address.
	TrustProxyHeaders bool `yaml:"trust_proxy_headers"`
}

type Database struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Name     string `yaml:"name"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	// Params are extra DSN parameters merged over the required defaults.
	Params          map[string]string `yaml:"params"`
	MaxOpenConns    int               `yaml:"max_open_conns"`
	MaxIdleConns    int               `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration     `yaml:"conn_max_lifetime"`
	// AutoMigrate runs pending migrations at startup.
	AutoMigrate bool `yaml:"auto_migrate"`
}

type Storage struct {
	// DataDir holds uploaded assets, run artifacts and generated PDFs.
	DataDir string `yaml:"data_dir"`
	// MaxUploadBytes caps a single uploaded file.
	MaxUploadBytes int64 `yaml:"max_upload_bytes"`
	// ProjectQuotaBytes caps total asset bytes per project. Zero means no cap.
	ProjectQuotaBytes int64 `yaml:"project_quota_bytes"`
}

type Auth struct {
	CookieName string        `yaml:"cookie_name"`
	SessionTTL time.Duration `yaml:"session_ttl"`
	// SecureCookies should be true wherever the site is served over HTTPS.
	SecureCookies bool `yaml:"secure_cookies"`
	BcryptCost    int  `yaml:"bcrypt_cost"`
	// AllowRegistration lets anyone create an account. When false only an
	// admin can add users.
	AllowRegistration bool `yaml:"allow_registration"`
	// BootstrapAdminEmail and BootstrapAdminPassword create the first admin at
	// startup when the users table is empty.
	BootstrapAdminEmail    string `yaml:"bootstrap_admin_email"`
	BootstrapAdminPassword string `yaml:"bootstrap_admin_password"`
}

type Engine struct {
	// RunnerPath is the simrunner executable. Empty resolves to simrunner
	// next to the server binary.
	RunnerPath string `yaml:"runner_path"`
	// MaxConcurrentRuns bounds simultaneous runner processes.
	MaxConcurrentRuns int `yaml:"max_concurrent_runs"`
	// RunTimeout kills a runner that exceeds it.
	RunTimeout time.Duration `yaml:"run_timeout"`
	// RunMemoryLimitBytes caps a runner process via a Windows job object.
	RunMemoryLimitBytes int64 `yaml:"run_memory_limit_bytes"`
}

type Plugins struct {
	// Enabled turns on server-side compilation of uploaded Go models. This
	// executes user-supplied code on the server: leave it off unless you have
	// read deploy/iis/SECURITY.md.
	Enabled      bool          `yaml:"enabled"`
	GoToolchain  string        `yaml:"go_toolchain"`
	BuildTimeout time.Duration `yaml:"build_timeout"`
	// BuildUser is an optional low-privilege Windows account builds run as.
	BuildUser string `yaml:"build_user"`
}

type Report struct {
	// BrowserPath is the Chromium-family executable used to print PDFs. Empty
	// means autodetect, which prefers the Edge that ships with Windows Server.
	BrowserPath string        `yaml:"browser_path"`
	Timeout     time.Duration `yaml:"timeout"`
}

type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	File   string `yaml:"file"`
}

// Default returns a configuration that runs out of the box for development.
func Default() Config {
	return Config{
		Server: Server{
			Addr:    "127.0.0.1:8080",
			BaseURL: "http://127.0.0.1:8080",
			WebRoot: "web/dist",

			ReadTimeout: 30 * time.Second,
			// Streaming endpoints (progress events, artifact chunks) must not
			// hit a write deadline, so there is deliberately no default.
			WriteTimeout:    0,
			IdleTimeout:     120 * time.Second,
			ShutdownTimeout: 20 * time.Second,
		},
		Database: Database{
			Host:            "127.0.0.1",
			Port:            3306,
			Name:            "simulation",
			User:            "simulation",
			MaxOpenConns:    25,
			MaxIdleConns:    10,
			ConnMaxLifetime: 30 * time.Minute,
			AutoMigrate:     true,
		},
		Storage: Storage{
			DataDir: "data",
			// 512 MiB covers a large GLB or a raw animation JSON export.
			MaxUploadBytes: 512 << 20,
		},
		Auth: Auth{
			CookieName:        "sim_session",
			SessionTTL:        14 * 24 * time.Hour,
			SecureCookies:     false,
			BcryptCost:        12,
			AllowRegistration: true,
		},
		Engine: Engine{
			MaxConcurrentRuns:   defaultConcurrentRuns(),
			RunTimeout:          2 * time.Hour,
			RunMemoryLimitBytes: 4 << 30,
		},
		Plugins: Plugins{
			Enabled:      false,
			GoToolchain:  "go",
			BuildTimeout: 2 * time.Minute,
		},
		Report: Report{Timeout: 3 * time.Minute},
		Log:    Log{Level: "info", Format: "text"},
	}
}

// Load reads path when it exists, applies environment overrides and validates
// the result. A missing file is not an error: defaults plus environment are
// enough to start.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		case errors.Is(err, os.ErrNotExist):
			// Fall through to defaults plus environment.
		default:
			return cfg, fmt.Errorf("read %s: %w", path, err)
		}
	}

	applyEnv(&cfg)

	if err := cfg.normalize(); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func applyEnv(cfg *Config) {
	envStr(&cfg.Server.Addr, "SERVER_ADDR")
	envStr(&cfg.Server.BaseURL, "BASE_URL")
	envStr(&cfg.Server.WebRoot, "WEB_ROOT")
	envBool(&cfg.Server.TrustProxyHeaders, "TRUST_PROXY_HEADERS")

	envStr(&cfg.Database.Host, "DB_HOST")
	envInt(&cfg.Database.Port, "DB_PORT")
	envStr(&cfg.Database.Name, "DB_NAME")
	envStr(&cfg.Database.User, "DB_USER")
	envStr(&cfg.Database.Password, "DB_PASSWORD")
	envBool(&cfg.Database.AutoMigrate, "DB_AUTO_MIGRATE")

	envStr(&cfg.Storage.DataDir, "DATA_DIR")
	envInt64(&cfg.Storage.MaxUploadBytes, "MAX_UPLOAD_BYTES")
	envInt64(&cfg.Storage.ProjectQuotaBytes, "PROJECT_QUOTA_BYTES")

	envStr(&cfg.Auth.CookieName, "COOKIE_NAME")
	envBool(&cfg.Auth.SecureCookies, "SECURE_COOKIES")
	envBool(&cfg.Auth.AllowRegistration, "ALLOW_REGISTRATION")
	envStr(&cfg.Auth.BootstrapAdminEmail, "BOOTSTRAP_ADMIN_EMAIL")
	envStr(&cfg.Auth.BootstrapAdminPassword, "BOOTSTRAP_ADMIN_PASSWORD")
	envDuration(&cfg.Auth.SessionTTL, "SESSION_TTL")

	envStr(&cfg.Engine.RunnerPath, "RUNNER_PATH")
	envInt(&cfg.Engine.MaxConcurrentRuns, "MAX_CONCURRENT_RUNS")
	envDuration(&cfg.Engine.RunTimeout, "RUN_TIMEOUT")

	envBool(&cfg.Plugins.Enabled, "PLUGINS_ENABLED")
	envStr(&cfg.Plugins.GoToolchain, "PLUGINS_GO_TOOLCHAIN")
	envStr(&cfg.Plugins.BuildUser, "PLUGINS_BUILD_USER")

	envStr(&cfg.Report.BrowserPath, "REPORT_BROWSER_PATH")

	envStr(&cfg.Log.Level, "LOG_LEVEL")
	envStr(&cfg.Log.Format, "LOG_FORMAT")
	envStr(&cfg.Log.File, "LOG_FILE")
}

func (c *Config) normalize() error {
	abs, err := filepath.Abs(c.Storage.DataDir)
	if err != nil {
		return fmt.Errorf("resolve data_dir: %w", err)
	}
	c.Storage.DataDir = abs

	if c.Server.WebRoot != "" {
		if abs, err := filepath.Abs(c.Server.WebRoot); err == nil {
			c.Server.WebRoot = abs
		}
	}
	c.Server.BaseURL = strings.TrimRight(c.Server.BaseURL, "/")
	c.Auth.BootstrapAdminEmail = strings.TrimSpace(c.Auth.BootstrapAdminEmail)
	return nil
}

func (c Config) Validate() error {
	var problems []string

	if c.Server.Addr == "" {
		problems = append(problems, "server.addr is empty")
	}
	if c.Server.BaseURL == "" {
		problems = append(problems, "server.base_url is empty")
	} else if _, err := url.Parse(c.Server.BaseURL); err != nil {
		problems = append(problems, "server.base_url is not a URL: "+err.Error())
	}
	if c.Database.Host == "" || c.Database.Name == "" || c.Database.User == "" {
		problems = append(problems, "database host, name and user are required")
	}
	if c.Database.Port <= 0 || c.Database.Port > 65535 {
		problems = append(problems, "database.port is out of range")
	}
	if c.Storage.MaxUploadBytes <= 0 {
		problems = append(problems, "storage.max_upload_bytes must be positive")
	}
	if c.Auth.BcryptCost < 10 || c.Auth.BcryptCost > 20 {
		problems = append(problems, "auth.bcrypt_cost must be between 10 and 20")
	}
	if c.Auth.SessionTTL <= 0 {
		problems = append(problems, "auth.session_ttl must be positive")
	}
	if c.Engine.MaxConcurrentRuns <= 0 {
		problems = append(problems, "engine.max_concurrent_runs must be positive")
	}
	if (c.Auth.BootstrapAdminEmail == "") != (c.Auth.BootstrapAdminPassword == "") {
		problems = append(problems, "bootstrap admin email and password must be set together")
	}
	if c.Auth.BootstrapAdminPassword != "" && len(c.Auth.BootstrapAdminPassword) < 12 {
		problems = append(problems, "bootstrap admin password must be at least 12 characters")
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// DSN builds the go-sql-driver connection string. The parameters set here are
// not optional: parseTime makes DATETIME scan into time.Time and a UTC
// connection time zone keeps stored timestamps unambiguous.
func (d Database) DSN() string {
	params := map[string]string{
		"parseTime":            "true",
		"loc":                  "UTC",
		"time_zone":            "'+00:00'",
		"charset":              "utf8mb4",
		"multiStatements":      "true",
		"interpolateParams":    "false",
		"allowNativePasswords": "true",
	}
	for k, v := range d.Params {
		params[k] = v
	}

	pairs := make([]string, 0, len(params))
	for k, v := range params {
		pairs = append(pairs, k+"="+url.QueryEscape(v))
	}
	// Deterministic order keeps logs and tests stable.
	sort.Strings(pairs)

	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?%s",
		d.User, d.Password, d.Host, d.Port, d.Name, strings.Join(pairs, "&"))
}

// RedactedDSN is the DSN with the password removed, safe to log.
func (d Database) RedactedDSN() string {
	clone := d
	if clone.Password != "" {
		clone.Password = "***"
	}
	return clone.DSN()
}

// CookieKey returns the 32-byte key used to sign cookies, read from
// SIM_COOKIE_KEY as 64 hex characters. When unset the caller is expected to
// generate a random key, which is fine for development but logs every user out
// on restart.
func CookieKey() ([]byte, bool, error) {
	raw := os.Getenv(EnvPrefix + "COOKIE_KEY")
	if raw == "" {
		return nil, false, nil
	}
	key, err := hex.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, false, fmt.Errorf("%sCOOKIE_KEY must be hex: %w", EnvPrefix, err)
	}
	if len(key) != 32 {
		return nil, false, fmt.Errorf("%sCOOKIE_KEY must decode to 32 bytes, got %d", EnvPrefix, len(key))
	}
	return key, true, nil
}

// defaultConcurrentRuns leaves half the cores for the server and the database.
func defaultConcurrentRuns() int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	return n
}

func envStr(dst *string, key string) {
	if v, ok := os.LookupEnv(EnvPrefix + key); ok {
		*dst = v
	}
}

func envInt(dst *int, key string) {
	if v, ok := os.LookupEnv(EnvPrefix + key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			*dst = n
		}
	}
}

func envInt64(dst *int64, key string) {
	if v, ok := os.LookupEnv(EnvPrefix + key); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			*dst = n
		}
	}
}

func envBool(dst *bool, key string) {
	if v, ok := os.LookupEnv(EnvPrefix + key); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			*dst = b
		}
	}
}

func envDuration(dst *time.Duration, key string) {
	if v, ok := os.LookupEnv(EnvPrefix + key); ok {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
			*dst = d
		}
	}
}
