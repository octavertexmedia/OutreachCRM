package openbao

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Nested paths merged under the base secret path (same pattern as ChannelManager).
var nestedSuffixes = []string{"app", "openai", "oauth", "email", "integrations"}

// Status describes the last ApplySecrets run.
type Status struct {
	Enabled bool     `json:"enabled"`
	Loaded  bool     `json:"loaded"`
	Addr    string   `json:"addr"`
	Path    string   `json:"path"`
	Keys    []string `json:"keys"`
	Error   string   `json:"error,omitempty"`
	Source  string   `json:"source"`
}

var lastStatus = Status{Source: "env"}

// Enabled reports whether OpenBao overlay is turned on.
func Enabled() bool {
	v := strings.ToLower(os.Getenv("OPENBAO_ENABLED"))
	return v == "1" || v == "true" || v == "yes"
}

// Required fails process startup when secrets cannot be loaded.
func Required() bool {
	v := strings.ToLower(os.Getenv("OPENBAO_REQUIRED"))
	return v == "1" || v == "true" || v == "yes"
}

// BaseSecretPath is mount-relative, e.g. vertexcrm/outreach/production.
func BaseSecretPath() string {
	root := os.Getenv("OPENBAO_SECRET_PATH")
	if root == "" {
		root = "vertexcrm/outreach"
	}
	envName := firstEnv("OPENBAO_ENVIRONMENT", "ENVIRONMENT")
	if envName == "" {
		envName = "production"
	}
	return strings.Trim(root, "/") + "/" + envName
}

// LastStatus returns the most recent ApplySecrets outcome.
func LastStatus() Status {
	return lastStatus
}

// ApplySecrets fetches KV secrets and injects them into the process environment.
// Bootstrap OPENBAO_* keys are never overwritten. Existing env values are kept
// unless OPENBAO_OVERWRITE_ENV is true (default).
func ApplySecrets() (Status, error) {
	st := Status{
		Enabled: Enabled(),
		Addr:    firstEnv("OPENBAO_ADDR", "BAO_ADDR", "VAULT_ADDR"),
		Path:    BaseSecretPath(),
		Source:  "env",
	}
	if !Enabled() {
		lastStatus = st
		return st, nil
	}

	overwrite := true
	if v := strings.ToLower(os.Getenv("OPENBAO_OVERWRITE_ENV")); v == "0" || v == "false" || v == "no" {
		overwrite = false
	}

	client := NewClientFromEnv()
	st.Addr = client.Addr
	secrets, err := collectSecrets(client)
	if err != nil {
		st.Error = err.Error()
		lastStatus = st
		slog.Warn("openbao secret load failed; using process env", "err", err, "path", st.Path)
		if Required() {
			return st, fmt.Errorf("OPENBAO_REQUIRED=true but secrets could not be loaded: %w", err)
		}
		return st, nil
	}

	applied := make([]string, 0, len(secrets))
	for key, value := range secrets {
		if strings.HasPrefix(key, "OPENBAO_") || strings.HasPrefix(key, "BAO_") || strings.HasPrefix(key, "VAULT_") {
			continue
		}
		if value == "" {
			continue
		}
		if !overwrite && os.Getenv(key) != "" {
			continue
		}
		_ = os.Setenv(key, value)
		applied = append(applied, key)
	}
	st.Loaded = true
	st.Keys = applied
	st.Source = "openbao"
	lastStatus = st
	slog.Info("openbao secrets loaded", "path", st.Path, "keys", len(applied), "addr", st.Addr)
	return st, nil
}

func collectSecrets(client *Client) (map[string]string, error) {
	path := BaseSecretPath()
	merged := map[string]string{}
	var firstErr error

	if data, err := client.ReadSecret(path); err == nil {
		for k, v := range data {
			merged[k] = v
		}
	} else {
		firstErr = err
	}

	for _, suffix := range nestedSuffixes {
		nested := path + "/" + suffix
		data, err := client.ReadSecret(nested)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for k, v := range data {
			merged[k] = v
		}
	}

	if len(merged) == 0 {
		if firstErr != nil {
			return nil, fmt.Errorf("no secrets found at %s: %w", path, firstErr)
		}
		return nil, fmt.Errorf("no secrets found at %s (or nested children)", path)
	}
	return merged, nil
}
