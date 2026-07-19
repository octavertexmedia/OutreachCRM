package search

import "strings"

// Field facet keys (search within a specific attribute).
const (
	FieldAny     = ""
	FieldName    = "name"
	FieldEmail   = "email"
	FieldPhone   = "phone"
	FieldWebsite = "website"
	FieldCompany = "company"
	FieldSubject = "subject"
	FieldNotes   = "notes"
)

// KindEmail is a virtual category: leads/accounts/inbox/queue that carry email.
const KindEmail Kind = "email"

// CategoryOption is one entity-type chip on the search page.
type CategoryOption struct {
	ID    string
	Label string
}

// FieldOption is one attribute chip on the search page.
type FieldOption struct {
	ID    string
	Label string
}

// Categories returns entity filters for the UI.
func Categories() []CategoryOption {
	return []CategoryOption{
		{ID: "", Label: "All"},
		{ID: string(KindLead), Label: "Leads"},
		{ID: string(KindCampaign), Label: "Campaigns"},
		{ID: string(KindEmail), Label: "Email"},
		{ID: string(KindReply), Label: "Inbox"},
		{ID: string(KindQueue), Label: "Queue"},
		{ID: string(KindTemplate), Label: "Templates"},
		{ID: string(KindAccount), Label: "Accounts"},
	}
}

// Fields returns attribute filters for the UI.
func Fields() []FieldOption {
	return []FieldOption{
		{ID: FieldAny, Label: "Any field"},
		{ID: FieldName, Label: "Name"},
		{ID: FieldEmail, Label: "Email"},
		{ID: FieldPhone, Label: "Phone"},
		{ID: FieldWebsite, Label: "Website"},
		{ID: FieldCompany, Label: "Company"},
		{ID: FieldSubject, Label: "Subject"},
		{ID: FieldNotes, Label: "Notes"},
	}
}

// ParseKind normalizes a kind query param.
func ParseKind(s string) Kind {
	s = strings.ToLower(strings.TrimSpace(s))
	switch Kind(s) {
	case KindLead, KindCampaign, KindTemplate, KindReply, KindQueue, KindAccount, KindEmail, "":
		return Kind(s)
	default:
		return ""
	}
}

// ParseField normalizes a field query param.
func ParseField(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case FieldName, FieldEmail, FieldPhone, FieldWebsite, FieldCompany, FieldSubject, FieldNotes:
		return s
	default:
		return FieldAny
	}
}

// KindMatches reports whether a document kind is included by the query filter.
func KindMatches(filter, doc Kind) bool {
	if filter == "" {
		return true
	}
	if filter == KindEmail {
		switch doc {
		case KindLead, KindAccount, KindReply, KindQueue:
			return true
		default:
			return false
		}
	}
	return filter == doc
}

// FacetTags builds a space-separated presence list for invert filtering.
func FacetTags(d Document) string {
	var tags []string
	if strings.TrimSpace(d.Name) != "" {
		tags = append(tags, "has_name")
	}
	if strings.TrimSpace(d.Email) != "" {
		tags = append(tags, "has_email")
	}
	if strings.TrimSpace(d.Phone) != "" {
		tags = append(tags, "has_phone")
	}
	if strings.TrimSpace(d.Website) != "" {
		tags = append(tags, "has_website")
	}
	if strings.TrimSpace(d.Company) != "" {
		tags = append(tags, "has_company")
	}
	if strings.TrimSpace(d.Subject) != "" {
		tags = append(tags, "has_subject")
	}
	if strings.TrimSpace(d.Notes) != "" {
		tags = append(tags, "has_notes")
	}
	return strings.Join(tags, " ")
}

// FieldText returns the text to emphasize for a field-scoped query.
func FieldText(d Document, field string) string {
	switch field {
	case FieldName:
		return d.Name
	case FieldEmail:
		return d.Email
	case FieldPhone:
		return d.Phone
	case FieldWebsite:
		return d.Website
	case FieldCompany:
		return d.Company
	case FieldSubject:
		return d.Subject
	case FieldNotes:
		return d.Notes
	default:
		return d.Content
	}
}

// HasField reports whether the document has a non-empty value for field.
func HasField(d Document, field string) bool {
	if field == FieldAny {
		return true
	}
	return strings.TrimSpace(FieldText(d, field)) != ""
}

// FilterResults applies kind/field filters client-side (safety net).
func FilterResults(results []Result, kind Kind, field string) []Result {
	if kind == "" && field == FieldAny {
		return results
	}
	out := results[:0]
	for _, r := range results {
		if !KindMatches(kind, r.Kind) {
			continue
		}
		if field != FieldAny && !strings.Contains(r.Facets, "has_"+field) {
			continue
		}
		out = append(out, r)
	}
	return out
}
