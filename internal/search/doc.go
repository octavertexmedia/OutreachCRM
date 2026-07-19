// Package search provides global hybrid search over CRM entities.
//
// Production builds (-tags zvec, the default make target) use Alibaba Zvec
// (https://github.com/alibaba/zvec): dense HNSW vectors + native FTS fused
// with MultiQuery RRF reranking. Lite builds (!zvec) fall back to SQLite FTS5.
package search

import (
	"fmt"
	"strings"
	"unicode"
)

// Kind identifies a searchable entity type.
type Kind string

const (
	KindLead     Kind = "lead"
	KindCampaign Kind = "campaign"
	KindTemplate Kind = "template"
	KindReply    Kind = "reply"
	KindQueue    Kind = "queue"
	KindAccount  Kind = "account"
)

// Document is one indexed record.
type Document struct {
	Kind        Kind
	EntityID    int64
	WorkspaceID int64
	OwnerID     int64
	Title       string
	Snippet     string
	Content     string
	Href        string
	// Structured fields for category / attribute filters.
	Name    string
	Email   string
	Phone   string
	Website string
	Company string
	Subject string
	Notes   string
}

// Result is a ranked hit returned to the UI.
type Result struct {
	Kind        Kind
	EntityID    int64
	WorkspaceID int64
	OwnerID     int64
	Title       string
	Snippet     string
	Href        string
	Score       float64
	Facets      string // e.g. "has_email has_phone"
}

// Query scopes a search request.
type Query struct {
	Text        string
	Kind        Kind   // "" = all; KindEmail = email-related entities
	Field       string // "" = any; email|phone|website|company|name|subject|notes
	WorkspaceID int64
	OwnerID     int64 // 0 = admin / all owners
	Admin       bool
	Limit       int
}

// Engine is the pluggable search backend.
type Engine interface {
	Backend() string
	Upsert(docs []Document) error
	Delete(kind Kind, entityID int64) error
	Search(q Query) ([]Result, error)
	Clear() error
	Close() error
}

// PK builds a stable primary key for a document.
// Zvec rejects some punctuation in PKs (e.g. ':'), so use underscore.
func PK(kind Kind, entityID int64) string {
	return fmt.Sprintf("%s_%d", kind, entityID)
}

// JoinText concatenates non-empty fields for the searchable body.
func JoinText(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p)
	}
	return b.String()
}

// Snippet truncates text for display.
func Snippet(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 {
		max = 160
	}
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// KindLabel is a short UI label.
func KindLabel(k Kind) string {
	switch k {
	case KindLead:
		return "Lead"
	case KindCampaign:
		return "Campaign"
	case KindTemplate:
		return "Template"
	case KindReply:
		return "Inbox"
	case KindQueue:
		return "Queue"
	case KindAccount:
		return "Account"
	default:
		return string(k)
	}
}

// sanitizeMatch keeps FTS queries safe (no control chars / quotes abuse).
func sanitizeMatch(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range q {
		if unicode.IsControl(r) {
			continue
		}
		switch r {
		case '"', '\'', '\\', ';', '{', '}', '[', ']':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
