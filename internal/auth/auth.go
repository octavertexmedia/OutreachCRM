package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
)

type ctxKey int

const userKey ctxKey = 1
const cookieName = "orc_session"

type Manager struct {
	Secret       []byte
	TTL          time.Duration
	CookieSecure bool
	limiter      *loginLimiter
	apiLimiter   *loginLimiter
}

func New(secret string, cookieSecure bool) *Manager {
	return &Manager{
		Secret:       []byte(secret),
		TTL:          7 * 24 * time.Hour,
		CookieSecure: cookieSecure,
		limiter:      newLoginLimiter(8, time.Minute),
		apiLimiter:   newLoginLimiter(120, time.Minute),
	}
}

func (m *Manager) AllowLogin(ip string) bool { return m.limiter.allow(ip) }
func (m *Manager) AllowAPI(ip string) bool   { return m.apiLimiter.allow(ip) }

func (m *Manager) SetSession(w http.ResponseWriter, u models.SessionUser) {
	exp := time.Now().UTC().Add(m.TTL)
	ws := u.WorkspaceID
	if ws == 0 {
		ws = 1
	}
	payload := strconv.FormatInt(u.ID, 10) + "|" + u.Role + "|" + u.Email + "|" + strconv.FormatInt(ws, 10) + "|" + exp.Format(time.RFC3339)
	mac := hmac.New(sha256.New, m.Secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	val := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: val, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: m.CookieSecure, Expires: exp,
	})
}

func (m *Manager) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: m.CookieSecure, MaxAge: -1})
}

func (m *Manager) UserFromRequest(r *http.Request) (models.SessionUser, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return models.SessionUser{}, false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return models.SessionUser{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return models.SessionUser{}, false
	}
	mac := hmac.New(sha256.New, m.Secret)
	mac.Write(raw)
	expect := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expect), []byte(parts[1])) {
		return models.SessionUser{}, false
	}
	fields := strings.Split(string(raw), "|")
	// v2: uid|role|email|ws|exp ; legacy v1: uid|role|email|exp
	var id int64
	var role, email string
	var ws int64 = 1
	var exp time.Time
	var err2 error
	switch len(fields) {
	case 5:
		id, err2 = strconv.ParseInt(fields[0], 10, 64)
		role, email = fields[1], fields[2]
		ws, _ = strconv.ParseInt(fields[3], 10, 64)
		exp, err = time.Parse(time.RFC3339, fields[4])
	case 4:
		id, err2 = strconv.ParseInt(fields[0], 10, 64)
		role, email = fields[1], fields[2]
		exp, err = time.Parse(time.RFC3339, fields[3])
	default:
		return models.SessionUser{}, false
	}
	if err != nil || err2 != nil || time.Now().UTC().After(exp) {
		return models.SessionUser{}, false
	}
	return models.SessionUser{ID: id, Role: role, Email: email, WorkspaceID: ws}, true
}

func UserFromContext(ctx context.Context) (models.SessionUser, bool) {
	u, ok := ctx.Value(userKey).(models.SessionUser)
	return u, ok
}

func WithUser(ctx context.Context, u models.SessionUser) context.Context {
	return context.WithValue(ctx, userKey, u)
}

func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/static/") ||
			path == "/" ||
			path == "/login" || path == "/login/totp" ||
			path == "/healthz" || path == "/readyz" || path == "/metrics" ||
			path == "/favicon.ico" || path == "/favicon.svg" || path == "/favicon-96x96.png" ||
			path == "/apple-touch-icon.png" || path == "/site.webmanifest" ||
			path == "/web-app-manifest-192x192.png" || path == "/web-app-manifest-512x512.png" ||
			strings.HasPrefix(path, "/u/") ||
			strings.HasPrefix(path, "/t/") ||
			strings.HasPrefix(path, "/webhooks/") {
			next.ServeHTTP(w, r)
			return
		}
		if !m.AllowAPI(ClientIP(r)) {
			http.Error(w, "rate limit", http.StatusTooManyRequests)
			return
		}
		u, ok := m.UserFromRequest(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok || !u.IsAdmin() {
			// Senders (and anyone else) must not land on admin pages.
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type loginLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	attempts map[string][]time.Time
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	return &loginLimiter{limit: limit, window: window, attempts: map[string][]time.Time{}}
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	arr := l.attempts[ip]
	kept := arr[:0]
	for _, t := range arr {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.attempts[ip] = kept
		return false
	}
	l.attempts[ip] = append(kept, now)
	return true
}

func (m *Manager) SignUnsubscribe(leadID, campaignID int64) string {
	payload := strconv.FormatInt(leadID, 10) + ":" + strconv.FormatInt(campaignID, 10)
	mac := hmac.New(sha256.New, m.Secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

func (m *Manager) VerifyUnsubscribe(token string) (leadID, campaignID int64, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return 0, 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, 0, false
	}
	mac := hmac.New(sha256.New, m.Secret)
	mac.Write(raw)
	expect := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expect), []byte(parts[1])) {
		return 0, 0, false
	}
	fields := strings.Split(string(raw), ":")
	if len(fields) != 2 {
		return 0, 0, false
	}
	leadID, err = strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	campaignID, err = strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return leadID, campaignID, true
}

// SignTrack signs open (o) or click (c) tokens. dest is empty for opens.
func (m *Manager) SignTrack(kind string, leadID, campaignID int64, dest string) string {
	if kind != "o" && kind != "c" {
		kind = "o"
	}
	payload := kind + ":" + strconv.FormatInt(leadID, 10) + ":" + strconv.FormatInt(campaignID, 10) + ":" + base64.RawURLEncoding.EncodeToString([]byte(dest))
	mac := hmac.New(sha256.New, m.Secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// VerifyTrack returns kind (o|c), lead, campaign, and optional click destination.
func (m *Manager) VerifyTrack(token string) (kind string, leadID, campaignID int64, dest string, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", 0, 0, "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", 0, 0, "", false
	}
	mac := hmac.New(sha256.New, m.Secret)
	mac.Write(raw)
	expect := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expect), []byte(parts[1])) {
		return "", 0, 0, "", false
	}
	fields := strings.SplitN(string(raw), ":", 4)
	if len(fields) != 4 {
		return "", 0, 0, "", false
	}
	kind = fields[0]
	leadID, err = strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return "", 0, 0, "", false
	}
	campaignID, err = strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", 0, 0, "", false
	}
	if fields[3] != "" {
		b, err := base64.RawURLEncoding.DecodeString(fields[3])
		if err == nil {
			dest = string(b)
		}
	}
	return kind, leadID, campaignID, dest, true
}
