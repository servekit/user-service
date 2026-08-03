package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	content := `
server:
  grpc: ":19094"
  http: ":18084"
database:
  driver: postgres
  postgres:
    host: "127.0.0.1"
    port: 5432
    user: "test"
    password: "test"
    dbname: "test_db"
    sslmode: "disable"
  max_open_conns: 25
  max_idle_conns: 10
  conn_max_lifetime: "5m"
  log_level: "warn"
  slow_threshold: "200ms"
  skip_default_tx: true
redis:
  addr: "localhost:6379"
session:
  ttl: "168h"
  key_prefix: "user:session"
  user_sessions_prefix: "user:user_sessions"
rbac:
  cache:
    user_perms_ttl: "10m"
  user_perms_prefix: "user:rbac:user_perms"
third_party:
  gid:
    mode: "module"
    gid:
      snowflake:
        machine_id: 1
        start_time: "2024-01-01T00:00:00Z"
log:
  level: "info"
  format: "json"
`
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(content)
	tmpFile.Close()

	t.Setenv("USER_SERVICE_CONFIG", tmpFile.Name())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.GRPC != ":19094" {
		t.Errorf("Server.GRPC = %q, want :19094", cfg.Server.GRPC)
	}
	if cfg.Database.Postgres.Port != 5432 {
		t.Errorf("Database.Port = %d, want 5432", cfg.Database.Postgres.Port)
	}
	if cfg.Database.ConnMaxLifetime != 5*time.Minute {
		t.Errorf("ConnMaxLifetime = %v, want 5m", cfg.Database.ConnMaxLifetime)
	}
	if cfg.Session.TTL != 168*time.Hour {
		t.Errorf("Session.TTL = %v, want 168h", cfg.Session.TTL)
	}
	if cfg.RBAC.Cache.UserPermsTTL != 10*time.Minute {
		t.Errorf("RBAC.Cache.UserPermsTTL = %v, want 10m", cfg.RBAC.Cache.UserPermsTTL)
	}
	if cfg.ThirdParty.GID.Config.Snowflake.MachineID != 1 {
		t.Errorf("ThirdParty.GID.Config.Snowflake.MachineID = %d, want 1", cfg.ThirdParty.GID.Config.Snowflake.MachineID)
	}
}

func TestLoadDefaults(t *testing.T) {
	// Minimal config — only required fields. Default-tagged fields should be
	// populated automatically.
	content := `
database:
  driver: postgres
  postgres:
    host: "127.0.0.1"
    user: "test"
    password: "test"
    dbname: "test_db"
redis:
  addr: "localhost:6379"
`
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(content)
	tmpFile.Close()

	t.Setenv("USER_SERVICE_CONFIG", tmpFile.Name())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Session.TTL != 168*time.Hour {
		t.Errorf("Session.TTL default = %v, want 168h", cfg.Session.TTL)
	}
	if cfg.Session.MaxSessions != 5 {
		t.Errorf("Session.MaxSessions default = %d, want 5", cfg.Session.MaxSessions)
	}
	if cfg.Session.KeyPrefix != "user:session" {
		t.Errorf("Session.KeyPrefix default = %q, want %q", cfg.Session.KeyPrefix, "user:session")
	}
	if cfg.Session.UserSessionsPrefix != "user:user_sessions" {
		t.Errorf("Session.UserSessionsPrefix default = %q, want %q", cfg.Session.UserSessionsPrefix, "user:user_sessions")
	}
	if cfg.RBAC.Cache.UserPermsTTL != 10*time.Minute {
		t.Errorf("RBAC.Cache.UserPermsTTL default = %v, want 10m", cfg.RBAC.Cache.UserPermsTTL)
	}
	if cfg.RBAC.Cache.GroupUserPermsTTL != 10*time.Minute {
		t.Errorf("RBAC.Cache.GroupUserPermsTTL default = %v, want 10m", cfg.RBAC.Cache.GroupUserPermsTTL)
	}
	if cfg.RBAC.UserPermsPrefix != "user:rbac:user_perms" {
		t.Errorf("RBAC.UserPermsPrefix default = %q, want %q", cfg.RBAC.UserPermsPrefix, "user:rbac:user_perms")
	}
	if cfg.RBAC.GroupUserPermsPrefix != "user:rbac:group_user_perms" {
		t.Errorf("RBAC.GroupUserPermsPrefix default = %q, want %q", cfg.RBAC.GroupUserPermsPrefix, "user:rbac:group_user_perms")
	}
	if cfg.ThirdParty.GID.Mode != "" {
		t.Errorf("ThirdParty.GID.Mode default = %q, want %q (no default; constructors treat empty as module)", cfg.ThirdParty.GID.Mode, "")
	}
	if cfg.ThirdParty.GID.Config.Snowflake.MachineID != 1 {
		t.Errorf("ThirdParty.GID.Config.Snowflake.MachineID default = %d, want 1", cfg.ThirdParty.GID.Config.Snowflake.MachineID)
	}
}

// loadEnvFile parses a dotenv file and sets each KEY=VALUE into the test
// environment. Mirrors docker-compose `env_file` semantics: blank lines and
// lines starting with '#' are skipped, inline comments are NOT supported (the
// value is everything after the first '='), and quotes are not stripped.
func loadEnvFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			t.Fatalf("malformed env line (no '='): %q", line)
		}
		key := strings.TrimSpace(line[:idx])
		if key == "" {
			t.Fatalf("empty key in env line: %q", line)
		}
		t.Setenv(key, line[idx+1:])
	}
}

// TestExampleConfigsAreLoadable guards that config.example.yaml + .env.example
// stay self-consistent: loads the YAML with every ${VAR} resolved from
// .env.example and asserts Load() succeeds with real (expanded) values,
// including the comma-separated OAuth allowed_redirect_urls list.
func TestExampleConfigsAreLoadable(t *testing.T) {
	root := filepath.Join("..", "..")
	loadEnvFile(t, filepath.Join(root, ".env.example"))
	t.Setenv("USER_SERVICE_CONFIG", filepath.Join(root, "config.example.yaml"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("config.example.yaml + .env.example must load: %v", err)
	}

	// Spot-check that ${VAR} was actually expanded, not left literal.
	if cfg.Server.GRPC != ":19094" {
		t.Errorf("Server.GRPC = %q, want %q", cfg.Server.GRPC, ":19094")
	}
	if cfg.Database.Postgres.Host != "127.0.0.1" {
		t.Errorf("Database.Host = %q, want 127.0.0.1", cfg.Database.Postgres.Host)
	}
	if cfg.Session.TTL != 168*time.Hour {
		t.Errorf("Session.TTL = %v, want 168h", cfg.Session.TTL)
	}
	// OAuth credential + redirect URL expanded.
	if cfg.OAuth.GitHub.RedirectURL != "https://auth.corp.com/oauth/callback" {
		t.Errorf("OAuth.GitHub.RedirectURL = %q", cfg.OAuth.GitHub.RedirectURL)
	}
	// Comma-separated list split into []string.
	if got := len(cfg.OAuth.GitHub.AllowedRedirectURLs); got != 2 {
		t.Errorf("len(OAuth.GitHub.AllowedRedirectURLs) = %d, want 2 (comma-split failed?)", got)
	}
	if cfg.ThirdParty.Message.Target != "localhost:9001" {
		t.Errorf("ThirdParty.Message.Target = %q, want localhost:9001", cfg.ThirdParty.Message.Target)
	}
}
