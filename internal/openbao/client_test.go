package openbao

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestAppRoleLoginAndReadKV(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/approle/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["role_id"] != "rid" || body["secret_id"] != "sid" {
			http.Error(w, `{"errors":["bad credentials"]}`, http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth": map[string]any{"client_token": "tok-123"},
		})
	})
	mux.HandleFunc("/v1/secret/data/vertexcrm/outreach/production", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok-123" {
			http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"data": map[string]any{
					"SESSION_SECRET": "from-bao",
					"ENCRYPTION_KEY": "enc-key",
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("OPENBAO_ENABLED", "true")
	t.Setenv("OPENBAO_REQUIRED", "true")
	t.Setenv("OPENBAO_ADDR", srv.URL)
	t.Setenv("OPENBAO_ROLE_ID", "rid")
	t.Setenv("OPENBAO_SECRET_ID", "sid")
	t.Setenv("OPENBAO_SECRET_PATH", "vertexcrm/outreach")
	t.Setenv("OPENBAO_ENVIRONMENT", "production")
	t.Setenv("OPENBAO_OVERWRITE_ENV", "true")
	_ = os.Unsetenv("SESSION_SECRET")

	st, err := ApplySecrets()
	if err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}
	if !st.Loaded || st.Source != "openbao" {
		t.Fatalf("status=%+v", st)
	}
	if got := os.Getenv("SESSION_SECRET"); got != "from-bao" {
		t.Fatalf("SESSION_SECRET=%q", got)
	}
	if got := os.Getenv("ENCRYPTION_KEY"); got != "enc-key" {
		t.Fatalf("ENCRYPTION_KEY=%q", got)
	}
}

func TestDisabledSkipsNetwork(t *testing.T) {
	t.Setenv("OPENBAO_ENABLED", "false")
	st, err := ApplySecrets()
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || st.Loaded {
		t.Fatalf("status=%+v", st)
	}
}
