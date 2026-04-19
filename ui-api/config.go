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

	UIOrigin   string `json:"ui_origin"`
	APIBaseURL string `json:"api_base_url"`

	SessionSecretEnv string `json:"session_secret_env"`
	SessionTTLHours  int    `json:"session_ttl_hours"`

	GoogleClientID        string `json:"google_client_id"`
	GoogleClientSecretEnv string `json:"google_client_secret_env"`

	MicrosoftClientID        string `json:"microsoft_client_id"`
	MicrosoftClientSecretEnv string `json:"microsoft_client_secret_env"`
	MicrosoftTenant          string `json:"microsoft_tenant"`
}

type Resolved struct {
	Cfg             Config
	SessionSecret   []byte
	GoogleID        string
	GoogleSecret    string
	MicrosoftID     string
	MicrosoftSecret string
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
		r.Cfg = c
	}
	return r, nil
}
