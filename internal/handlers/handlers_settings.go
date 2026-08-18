package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/imapsync"
	"github.com/manishkumar/outreachcrm/internal/llm"
	"github.com/manishkumar/outreachcrm/internal/mail"
	"github.com/manishkumar/outreachcrm/internal/models"
)

func (s *Server) settingsEmailGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	ai, _ := s.Store.GetWorkspaceAI(u.WorkspaceID)
	mkt, _ := s.Store.GetMarketingSMTP(u.WorkspaceID)
	s.render(w, "settings_email.html", map[string]any{
		"Nav": "settings", "User": u,
		"AI": ai, "Marketing": mkt,
		"AIMode":       s.Cfg.AIMode,
		"HasEnvKey":    s.Cfg.OpenAIAPIKey != "",
		"HasWSKey":     ai.OpenAIKeyEnc != "",
		"DefaultSys":   llm.DefaultSystemPrompt(),
		"ESPProviders": []string{"smtp", "brevo", "sendgrid", "mailgun", "postmark", "ses"},
		"Saved":        r.URL.Query().Get("saved"),
	})
}

func (s *Server) settingsAIPost(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	existing, _ := s.Store.GetWorkspaceAI(u.WorkspaceID)
	keyEnc := existing.OpenAIKeyEnc
	if raw := strings.TrimSpace(r.FormValue("openai_api_key")); raw != "" && s.Box != nil {
		if enc, err := s.Box.Encrypt(raw); err == nil {
			keyEnc = enc
		}
	}
	err := s.Store.UpsertWorkspaceAI(models.WorkspaceAI{
		WorkspaceID:    u.WorkspaceID,
		SystemPrompt:   strings.TrimSpace(r.FormValue("system_prompt")),
		BusinessPrompt: strings.TrimSpace(r.FormValue("business_prompt")),
		OpenAIKeyEnc:   keyEnc,
		OpenAIBaseURL:  strings.TrimSpace(r.FormValue("openai_base_url")),
		OpenAIModel:    strings.TrimSpace(r.FormValue("openai_model")),
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "settings.ai", "workspace", strconv.FormatInt(u.WorkspaceID, 10), "")
	http.Redirect(w, r, "/settings/email?saved=ai", http.StatusSeeOther)
}

func (s *Server) settingsMarketingPost(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	existing, _ := s.Store.GetMarketingSMTP(u.WorkspaceID)
	provider := strings.ToLower(strings.TrimSpace(r.FormValue("provider")))
	if provider == "" {
		provider = models.ProviderSMTP
	}
	host := strings.TrimSpace(r.FormValue("host"))
	port, _ := strconv.Atoi(r.FormValue("port"))
	if host == "" || port == 0 {
		h, p := models.MarketingPreset(provider)
		if host == "" {
			host = h
		}
		if port == 0 {
			port = p
		}
	}
	passEnc, keyEnc := existing.PasswordEnc, existing.APIKeyEnc
	if raw := r.FormValue("password"); raw != "" && s.Box != nil {
		if enc, err := s.Box.Encrypt(raw); err == nil {
			passEnc = enc
		}
	}
	if raw := r.FormValue("api_key"); raw != "" && s.Box != nil {
		if enc, err := s.Box.Encrypt(raw); err == nil {
			keyEnc = enc
		}
	}
	quota, _ := strconv.Atoi(r.FormValue("daily_quota"))
	err := s.Store.UpsertMarketingSMTP(models.MarketingSMTP{
		WorkspaceID: u.WorkspaceID,
		Provider:    provider,
		Host:        host,
		Port:        port,
		Username:    strings.TrimSpace(r.FormValue("username")),
		PasswordEnc: passEnc,
		APIKeyEnc:   keyEnc,
		FromEmail:   strings.TrimSpace(r.FormValue("from_email")),
		FromName:    strings.TrimSpace(r.FormValue("from_name")),
		DailyQuota:  quota,
		Enabled:     r.FormValue("enabled") == "1",
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "settings.marketing_smtp", "workspace", strconv.FormatInt(u.WorkspaceID, 10), provider)
	http.Redirect(w, r, "/settings/email?saved=marketing", http.StatusSeeOther)
}

func (s *Server) accountsGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	accounts, err := s.Store.ListAccounts(u.IsAdmin(), u.ID, u.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	type accView struct {
		models.EmailAccount
		Role string
	}
	views := make([]accView, 0, len(accounts))
	for _, a := range accounts {
		views = append(views, accView{EmailAccount: a, Role: mail.AccountRole(a)})
	}
	mkt, _ := s.Store.GetMarketingSMTP(u.WorkspaceID)
	s.render(w, "accounts.html", map[string]any{
		"Accounts": views, "Nav": "accounts", "User": u,
		"GoogleEnabled":  s.OAuth != nil && s.OAuth.Google != nil,
		"MSEnabled":      s.OAuth != nil && s.OAuth.Microsoft != nil,
		"MailboxPresets": imapsync.Presets(),
		"Marketing":      mkt,
		"MarketingOK":    mkt.Configured(),
	})
}
