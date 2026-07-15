// Package openbao loads KV secrets from OpenBao (Vault-compatible API).
package openbao

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Client talks to OpenBao / Vault KV v2 via AppRole or token auth.
type Client struct {
	Addr       string
	MountPoint string
	Namespace  string
	Token      string
	RoleID     string
	SecretID   string
	HTTP       *http.Client
}

type loginResponse struct {
	Auth struct {
		ClientToken string `json:"client_token"`
	} `json:"auth"`
	Errors []string `json:"errors"`
}

type kvResponse struct {
	Data struct {
		Data map[string]any `json:"data"`
	} `json:"data"`
	Errors []string `json:"errors"`
}

// NewClientFromEnv builds a client from OPENBAO_* / BAO_* / VAULT_* env vars.
func NewClientFromEnv() *Client {
	timeoutSec := 10
	if v := firstEnv("OPENBAO_TIMEOUT", "BAO_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeoutSec = n
		}
	}
	verify := true
	if v := strings.ToLower(firstEnv("OPENBAO_TLS_VERIFY", "BAO_TLS_VERIFY")); v == "0" || v == "false" || v == "no" {
		verify = false
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if !verify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // optional op toggle
	}
	addr := firstEnv("OPENBAO_ADDR", "BAO_ADDR", "VAULT_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:8200"
	}
	mount := firstEnv("OPENBAO_MOUNT_POINT", "BAO_MOUNT_POINT")
	if mount == "" {
		mount = "secret"
	}
	return &Client{
		Addr:       strings.TrimRight(addr, "/"),
		MountPoint: mount,
		Namespace:  firstEnv("OPENBAO_NAMESPACE", "BAO_NAMESPACE"),
		Token:      firstEnv("OPENBAO_TOKEN", "BAO_TOKEN", "VAULT_TOKEN"),
		RoleID:     firstEnv("OPENBAO_ROLE_ID", "BAO_ROLE_ID"),
		SecretID:   firstEnv("OPENBAO_SECRET_ID", "BAO_SECRET_ID"),
		HTTP:       &http.Client{Timeout: time.Duration(timeoutSec) * time.Second, Transport: tr},
	}
}

// Authenticate ensures c.Token is set via AppRole when needed.
func (c *Client) Authenticate() error {
	if c.Token != "" {
		return nil
	}
	if c.RoleID == "" || c.SecretID == "" {
		return fmt.Errorf("openbao: set OPENBAO_TOKEN or OPENBAO_ROLE_ID + OPENBAO_SECRET_ID")
	}
	body, _ := json.Marshal(map[string]string{
		"role_id":   c.RoleID,
		"secret_id": c.SecretID,
	})
	req, err := http.NewRequest(http.MethodPost, c.Addr+"/v1/auth/approle/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyNamespace(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("openbao approle login: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out loginResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("openbao login decode: %w", err)
	}
	if resp.StatusCode >= 300 || out.Auth.ClientToken == "" {
		msg := strings.Join(out.Errors, "; ")
		if msg == "" {
			msg = string(raw)
		}
		return fmt.Errorf("openbao login failed (%d): %s", resp.StatusCode, msg)
	}
	c.Token = out.Auth.ClientToken
	return nil
}

// ReadSecret reads KV v2 data at path relative to the mount.
func (c *Client) ReadSecret(path string) (map[string]string, error) {
	if err := c.Authenticate(); err != nil {
		return nil, err
	}
	path = strings.Trim(path, "/")
	url := fmt.Sprintf("%s/v1/%s/data/%s", c.Addr, strings.Trim(c.MountPoint, "/"), path)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", c.Token)
	c.applyNamespace(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openbao read %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("openbao: secret not found at %s", path)
	}
	var out kvResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("openbao read decode %s: %w", path, err)
	}
	if resp.StatusCode >= 300 {
		msg := strings.Join(out.Errors, "; ")
		if msg == "" {
			msg = string(raw)
		}
		return nil, fmt.Errorf("openbao read %s failed (%d): %s", path, resp.StatusCode, msg)
	}
	result := make(map[string]string, len(out.Data.Data))
	for k, v := range out.Data.Data {
		if v == nil {
			continue
		}
		result[k] = fmt.Sprint(v)
	}
	return result, nil
}

// Reachable returns true when sys/health responds (sealed/uninit still count as up).
func (c *Client) Reachable() bool {
	req, err := http.NewRequest(http.MethodGet, c.Addr+"/v1/sys/health", nil)
	if err != nil {
		return false
	}
	c.applyNamespace(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode < 500
}

func (c *Client) applyNamespace(req *http.Request) {
	if c.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.Namespace)
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
