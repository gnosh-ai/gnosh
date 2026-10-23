package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// DefaultMetricsURL is where opted-in usage metrics go when metrics_url is not
// set: klash's own analytics, never Charm's. A custom collector overrides it
// with metrics_url.
const DefaultMetricsURL = "https://klash.gno.run"

// metricsConsentFile holds this machine's answer to the first-run analytics
// question, in the global data directory, so it is asked once per machine and
// not once per project.
const metricsConsentFile = "metrics-consent.json"

type metricsConsent struct {
	Allow     bool   `json:"allow"`
	DecidedAt string `json:"decided_at"`
}

func metricsConsentPath() string {
	// GlobalConfigData returns the global data config file; the consent file
	// sits beside it, in the same per-machine directory.
	return filepath.Join(filepath.Dir(GlobalConfigData()), metricsConsentFile)
}

// MetricsConsent reports this machine's stored answer: decided is false until
// the first-run question is answered, and allow is the answer. An unreadable
// or malformed file reads as undecided, so a bad file never turns collection
// on.
func MetricsConsent() (decided, allow bool) {
	data, err := os.ReadFile(metricsConsentPath())
	if err != nil {
		return false, false
	}
	var c metricsConsent
	if json.Unmarshal(data, &c) != nil {
		return false, false
	}
	return true, c.Allow
}

// MetricsConsentDecided reports whether this machine has answered the
// first-run question.
func MetricsConsentDecided() bool {
	decided, _ := MetricsConsent()
	return decided
}

// RecordMetricsConsent stores this machine's answer so it is not asked again.
func RecordMetricsConsent(allow bool) error {
	p := metricsConsentPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(metricsConsent{
		Allow:     allow,
		DecidedAt: time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
