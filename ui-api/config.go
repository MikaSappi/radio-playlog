package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
)

// Config is the on-disk configuration loaded from configuration.json.
// Client IDs are public (they leak into OAuth redirect URLs anyway) and
// live in config directly. Client secrets and the session signing key
// come from env vars referenced by *_env fields.
type Config struct {
	GCPProject      string `json:"gcp_project"`
	BQDataset       string `json:"bq_dataset"`
	BQTableUsers    string `json:"bq_table_users"`
	BQTableAPIKeys  string `json:"bq_table_api_keys"`
	BQTableSettings string `json:"bq_table_settings"`
	BQTablePlays    string `json:"bq_table_plays"`
	BQTableAlerts   string `json:"bq_table_alerts"`

	UIOrigin   string `json:"ui_origin"`
	APIBaseURL string `json:"api_base_url"`

	SessionSecretEnv string `json:"session_secret_env"`
	SessionTTLHours  int    `json:"session_ttl_hours"`

	GoogleClientID        string `json:"google_client_id"`
	GoogleClientSecretEnv string `json:"google_client_secret_env"`

	MicrosoftClientID        string `json:"microsoft_client_id"`
	MicrosoftClientSecretEnv string `json:"microsoft_client_secret_env"`
	MicrosoftTenant          string `json:"microsoft_tenant"`

	// SMTP email delivery for silence alerts and scheduled reports.
	// Values come from env (SMTPHostEnv etc.) so no secret is committed.
	// The minimum viable config is just SMTP_USER + SMTP_PASSWORD — the
	// host defaults to smtp.gmail.com:587 and From defaults to the user
	// address, which is what Gmail requires. Setting the host/port/from
	// envs lets you point at any other provider.
	SMTPHostEnv     string `json:"smtp_host_env"`
	SMTPPortEnv     string `json:"smtp_port_env"`
	SMTPUserEnv     string `json:"smtp_user_env"`
	SMTPPasswordEnv string `json:"smtp_password_env"`
	SMTPFromEnv     string `json:"smtp_from_env"`

	// SilenceAlertHours is how long a previously-active key must be
	// silent before an email is sent. Zero falls back to 1.
	SilenceAlertHours int `json:"silence_alert_hours"`
}

type Resolved struct {
	Cfg             Config
	SessionSecret   []byte
	GoogleID        string
	GoogleSecret    string
	MicrosoftID     string
	MicrosoftSecret string

	SMTPHost     string
	SMTPPort     string
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
}

func resolveConfigPath() string {
	var flagPath string
	flag.StringVar(&flagPath, "config", "", "path to configuration.json")
	flag.Parse()
	if flagPath != "" {
		return flagPath
	}
	if env := os.Getenv("UI_API_CONFIG"); env != "" {
		return env
	}
	return "configuration.json"
}

func loadConfig() (*Resolved, error) {
	path := resolveConfigPath()
	log.Printf("Loading config from %s", path)

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	r := &Resolved{Cfg: c}

	secret := os.Getenv(c.SessionSecretEnv)
	if secret == "" {
		return nil, fmt.Errorf("missing session secret in env var %s", c.SessionSecretEnv)
	}
	r.SessionSecret = []byte(secret)

	r.GoogleID = c.GoogleClientID
	r.GoogleSecret = os.Getenv(c.GoogleClientSecretEnv)
	r.MicrosoftID = c.MicrosoftClientID
	r.MicrosoftSecret = os.Getenv(c.MicrosoftClientSecretEnv)

	if r.GoogleID == "" && r.MicrosoftID == "" {
		return nil, fmt.Errorf("at least one of Google or Microsoft OAuth must be configured")
	}
	if c.SessionTTLHours <= 0 {
		c.SessionTTLHours = 168
	}
	if c.BQTablePlays == "" {
		c.BQTablePlays = "plays"
	}
	if c.BQTableAlerts == "" {
		c.BQTableAlerts = "key_alerts"
	}
	if c.SilenceAlertHours <= 0 {
		c.SilenceAlertHours = 1
	}
	r.Cfg = c

	r.SMTPHost = os.Getenv(c.SMTPHostEnv)
	r.SMTPPort = os.Getenv(c.SMTPPortEnv)
	r.SMTPUser = os.Getenv(c.SMTPUserEnv)
	r.SMTPPassword = os.Getenv(c.SMTPPasswordEnv)
	r.SMTPFrom = os.Getenv(c.SMTPFromEnv)
	// Default to Gmail SMTP so supplying just SMTP_USER + SMTP_PASSWORD
	// (a Gmail app password) is enough to send. From defaults to the
	// signed-in user address, which is what Gmail expects anyway — any
	// other From value would be rewritten or rejected.
	if r.SMTPHost == "" {
		r.SMTPHost = "smtp.gmail.com"
	}
	if r.SMTPPort == "" {
		r.SMTPPort = "587"
	}
	if r.SMTPFrom == "" {
		r.SMTPFrom = r.SMTPUser
	}
	return r, nil
}
