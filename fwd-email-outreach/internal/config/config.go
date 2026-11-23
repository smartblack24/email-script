package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds runtime settings for the outreach CLI and worker.
type Config struct {
	GmailCredentialsFile string
	GmailTokenFile       string
	DatabasePath         string
	TemplatesDir         string

	DailySendLimit         int
	HourlySendLimit        int
	MinSendIntervalSeconds int
	SendTimezone           string
	SendStartHour          int
	SendEndHour            int

	FollowupEnabled bool
	FollowupDays    []int

	DryRun bool

	SenderName           string
	SenderEmail          string
	BusinessName         string
	BusinessAddress      string
	OptOutEmail          string
	RetentionDays        int
	DefaultCampaignName  string
	ColumnMapOverrides   map[string]string
}

// Load reads configuration from the environment (and optional .env file).
func Load(envFiles ...string) (*Config, error) {
	if len(envFiles) > 0 {
		_ = godotenv.Load(envFiles...)
	} else {
		_ = godotenv.Load()
	}

	cfg := &Config{
		GmailCredentialsFile:   getEnv("GMAIL_CREDENTIALS_FILE", "credentials.json"),
		GmailTokenFile:         getEnv("GMAIL_TOKEN_FILE", "token.json"),
		DatabasePath:           getEnv("DATABASE_PATH", "data/outreach.db"),
		TemplatesDir:           getEnv("TEMPLATES_DIR", "templates"),
		DailySendLimit:         getEnvInt("DAILY_SEND_LIMIT", 50),
		HourlySendLimit:        getEnvInt("HOURLY_SEND_LIMIT", 10),
		MinSendIntervalSeconds: getEnvInt("MIN_SEND_INTERVAL_SECONDS", 90),
		SendTimezone:           getEnv("SEND_TIMEZONE", "America/New_York"),
		SendStartHour:          getEnvInt("SEND_START_HOUR", 9),
		SendEndHour:            getEnvInt("SEND_END_HOUR", 17),
		FollowupEnabled:        getEnvBool("FOLLOWUP_ENABLED", true),
		FollowupDays:           parseFollowupDays(getEnv("FOLLOWUP_DAYS", "0,4,9")),
		DryRun:                 getEnvBool("DRY_RUN", true),
		SenderName:             getEnv("SENDER_NAME", ""),
		SenderEmail:            getEnv("SENDER_EMAIL", ""),
		BusinessName:           getEnv("BUSINESS_NAME", "FWD Consulting Group"),
		BusinessAddress:        getEnv("BUSINESS_ADDRESS", ""),
		OptOutEmail:            getEnv("OPT_OUT_EMAIL", ""),
		RetentionDays:          getEnvInt("RETENTION_DAYS", 365),
		DefaultCampaignName:    getEnv("DEFAULT_CAMPAIGN_NAME", "default"),
		ColumnMapOverrides:     parseColumnMap(getEnv("COLUMN_MAP", "")),
	}

	if cfg.DailySendLimit < 1 {
		return nil, fmt.Errorf("DAILY_SEND_LIMIT must be >= 1")
	}
	if cfg.HourlySendLimit < 1 {
		return nil, fmt.Errorf("HOURLY_SEND_LIMIT must be >= 1")
	}
	if cfg.MinSendIntervalSeconds < 0 {
		return nil, fmt.Errorf("MIN_SEND_INTERVAL_SECONDS must be >= 0")
	}
	if cfg.SendStartHour < 0 || cfg.SendStartHour > 23 || cfg.SendEndHour < 0 || cfg.SendEndHour > 23 {
		return nil, fmt.Errorf("SEND_START_HOUR/SEND_END_HOUR must be 0-23")
	}
	if _, err := time.LoadLocation(cfg.SendTimezone); err != nil {
		return nil, fmt.Errorf("invalid SEND_TIMEZONE %q: %w", cfg.SendTimezone, err)
	}

	return cfg, nil
}

// ValidateForLiveSend ensures compliance identity fields are set before live sends.
func (c *Config) ValidateForLiveSend() error {
	var missing []string
	if strings.TrimSpace(c.SenderName) == "" {
		missing = append(missing, "SENDER_NAME")
	}
	if strings.TrimSpace(c.SenderEmail) == "" {
		missing = append(missing, "SENDER_EMAIL")
	}
	if strings.TrimSpace(c.BusinessName) == "" {
		missing = append(missing, "BUSINESS_NAME")
	}
	if strings.TrimSpace(c.BusinessAddress) == "" {
		missing = append(missing, "BUSINESS_ADDRESS")
	}
	if strings.TrimSpace(c.OptOutEmail) == "" {
		missing = append(missing, "OPT_OUT_EMAIL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("live send blocked: missing required identity/compliance config: %s", strings.Join(missing, ", "))
	}
	return nil
}

// MinSendInterval returns the configured delay between sends.
func (c *Config) MinSendInterval() time.Duration {
	return time.Duration(c.MinSendIntervalSeconds) * time.Second
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return b
}

func parseFollowupDays(raw string) []int {
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	seen := map[int]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return []int{0, 4, 9}
	}
	return out
}

func parseColumnMap(raw string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	for _, pair := range strings.Split(raw, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		out[normalizeHeader(kv[0])] = strings.TrimSpace(kv[1])
	}
	return out
}

func normalizeHeader(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "/", "")
	return s
}
