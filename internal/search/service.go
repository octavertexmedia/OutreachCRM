package search

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/store"
)

// Service owns the search engine and reindexes from SQLite.
type Service struct {
	eng      Engine
	store    *store.Store
	embedder Embedder
	mu       sync.Mutex
}

// Open creates the search service under dataDir/search (engine-specific files inside).
func Open(dataDir string, st *store.Store, embedder Embedder) (*Service, error) {
	if embedder == nil {
		embedder = HashEmbedder{}
	}
	root := filepath.Join(dataDir, "search")
	eng, err := openEngine(root, embedder)
	if err != nil {
		return nil, err
	}
	s := &Service{eng: eng, store: st, embedder: embedder}
	if n, err := s.Reindex(); err != nil {
		slog.Warn("search reindex", "err", err)
	} else {
		slog.Info("search ready", "backend", eng.Backend(), "docs", n, "embedder", embedder.Name())
	}
	return s, nil
}

// Backend returns the active engine name.
func (s *Service) Backend() string {
	if s == nil || s.eng == nil {
		return "none"
	}
	return s.eng.Backend()
}

// Close releases the engine.
func (s *Service) Close() error {
	if s == nil || s.eng == nil {
		return nil
	}
	return s.eng.Close()
}

// Search runs a scoped query.
func (s *Service) Search(q Query) ([]Result, error) {
	if s == nil || s.eng == nil {
		return nil, fmt.Errorf("search not available")
	}
	q.Kind = ParseKind(string(q.Kind))
	q.Field = ParseField(q.Field)
	results, err := s.eng.Search(q)
	if err != nil {
		return nil, err
	}
	return FilterResults(results, q.Kind, q.Field), nil
}

// Upsert indexes documents immediately.
func (s *Service) Upsert(docs []Document) {
	if s == nil || s.eng == nil || len(docs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.eng.Upsert(docs); err != nil {
		slog.Warn("search upsert", "err", err, "n", len(docs))
	}
}

// Delete removes one entity from the index.
func (s *Service) Delete(kind Kind, entityID int64) {
	if s == nil || s.eng == nil {
		return
	}
	if err := s.eng.Delete(kind, entityID); err != nil {
		slog.Warn("search delete", "err", err, "kind", kind, "id", entityID)
	}
}

// Reindex rebuilds the full index from the primary store.
func (s *Service) Reindex() (int, error) {
	if s == nil || s.eng == nil || s.store == nil {
		return 0, fmt.Errorf("search not available")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.eng.Clear(); err != nil {
		return 0, err
	}
	docs := collectDocuments(s.store)
	if err := s.eng.Upsert(docs); err != nil {
		return 0, err
	}
	return len(docs), nil
}

func collectDocuments(st *store.Store) []Document {
	var docs []Document

	leads, err := st.ListLeads(true, 0)
	if err == nil {
		for _, l := range leads {
			docs = append(docs, DocFromLead(l))
		}
	}

	camps, err := st.ListCampaigns(true, 0)
	if err == nil {
		for _, c := range camps {
			docs = append(docs, DocFromCampaign(c))
		}
	}

	// Templates are per-workspace; scan known workspaces + default 1.
	wsIDs := []int64{1}
	if wss, err := st.ListWorkspaces(); err == nil {
		wsIDs = wsIDs[:0]
		for _, w := range wss {
			wsIDs = append(wsIDs, w.ID)
		}
		if len(wsIDs) == 0 {
			wsIDs = []int64{1}
		}
	}
	seenTpl := map[int64]bool{}
	for _, ws := range wsIDs {
		tpls, err := st.ListTemplates(ws)
		if err != nil {
			continue
		}
		for _, t := range tpls {
			if seenTpl[t.ID] {
				continue
			}
			seenTpl[t.ID] = true
			docs = append(docs, DocFromTemplate(t))
		}
	}

	replies, err := st.ListReplies(true, 0)
	if err == nil {
		for _, r := range replies {
			docs = append(docs, DocFromReply(r))
		}
	}

	queue, err := st.ListQueue(true, 0, 500)
	if err == nil {
		campCache := map[int64]models.Campaign{}
		for _, m := range queue {
			doc := DocFromQueue(m)
			if c, ok := campCache[m.CampaignID]; ok {
				doc.WorkspaceID = c.WorkspaceID
				doc.OwnerID = c.OwnerID
			} else if c, err := st.GetCampaign(m.CampaignID); err == nil {
				campCache[m.CampaignID] = c
				doc.WorkspaceID = c.WorkspaceID
				doc.OwnerID = c.OwnerID
			}
			docs = append(docs, doc)
		}
	}

	accts, err := st.ListAccounts(true, 0)
	if err == nil {
		for _, a := range accts {
			docs = append(docs, DocFromAccount(a))
		}
	}

	return docs
}

// DocFromLead builds a searchable lead document.
func DocFromLead(l models.Lead) Document {
	title := l.Name
	if title == "" {
		title = l.Email
	}
	if title == "" {
		title = fmt.Sprintf("Lead #%d", l.ID)
	}
	content := JoinText(l.Name, l.Email, l.Company, l.Title, l.Website, l.Phone, l.Category, l.Source, l.Notes, l.DraftSubject, l.DraftBody, l.IssuesJSON)
	return Document{
		Kind: KindLead, EntityID: l.ID, WorkspaceID: l.WorkspaceID, OwnerID: l.OwnerID,
		Title: title, Snippet: Snippet(JoinText(l.Company, l.Email, l.Title), 160),
		Content: content, Href: fmt.Sprintf("/leads#lead-%d", l.ID),
		Name: l.Name, Email: l.Email, Phone: l.Phone, Website: l.Website,
		Company: l.Company, Subject: l.DraftSubject, Notes: l.Notes,
	}
}

// DocFromCampaign builds a searchable campaign document.
func DocFromCampaign(c models.Campaign) Document {
	title := c.Name
	if title == "" {
		title = fmt.Sprintf("Campaign #%d", c.ID)
	}
	return Document{
		Kind: KindCampaign, EntityID: c.ID, WorkspaceID: c.WorkspaceID, OwnerID: c.OwnerID,
		Title: title, Snippet: Snippet(JoinText(c.Status, c.Timezone), 160),
		Content: JoinText(c.Name, c.Status, c.Timezone), Href: fmt.Sprintf("/campaigns#campaign-%d", c.ID),
		Name: c.Name,
	}
}

// DocFromTemplate builds a searchable template document.
func DocFromTemplate(t models.EmailTemplate) Document {
	title := t.Name
	if title == "" {
		title = t.Subject
	}
	if title == "" {
		title = fmt.Sprintf("Template #%d", t.ID)
	}
	return Document{
		Kind: KindTemplate, EntityID: t.ID, WorkspaceID: t.WorkspaceID, OwnerID: 0,
		Title: title, Snippet: Snippet(t.Subject, 160),
		Content: JoinText(t.Name, t.Subject, t.Body), Href: "/templates",
		Name: t.Name, Subject: t.Subject, Notes: t.Body,
	}
}

// DocFromReply builds a searchable inbox document.
func DocFromReply(r models.InboundReply) Document {
	title := r.Subject
	if title == "" {
		title = r.FromEmail
	}
	if title == "" {
		title = fmt.Sprintf("Reply #%d", r.ID)
	}
	var ws, owner int64 = 1, 0
	if r.WorkspaceID != nil {
		ws = *r.WorkspaceID
	}
	if r.OwnerID != nil {
		owner = *r.OwnerID
	}
	href := "/inbox"
	if r.HITLStatus == models.HITLNeedsReview || r.Intent == "positive" {
		href = "/hitl"
	}
	return Document{
		Kind: KindReply, EntityID: r.ID, WorkspaceID: ws, OwnerID: owner,
		Title: title, Snippet: Snippet(JoinText(r.FromEmail, r.LeadName, r.Intent), 160),
		Content: JoinText(r.FromEmail, r.LeadName, r.Subject, r.Body, r.Intent, r.ThreadID), Href: href,
		Name: r.LeadName, Email: r.FromEmail, Subject: r.Subject, Notes: r.Body,
	}
}

// DocFromQueue builds a searchable outbound queue document.
func DocFromQueue(m models.OutboundMessage) Document {
	title := m.Subject
	if title == "" {
		title = m.ToEmail
	}
	if title == "" {
		title = fmt.Sprintf("Queue #%d", m.ID)
	}
	return Document{
		Kind: KindQueue, EntityID: m.ID, WorkspaceID: 1, OwnerID: 0,
		Title: title, Snippet: Snippet(JoinText(m.ToEmail, m.Status), 160),
		Content: JoinText(m.ToEmail, m.Subject, m.Body, m.Status, m.Error, m.LastError),
		Href:    fmt.Sprintf("/queue#queue-%d", m.ID),
		Email: m.ToEmail, Subject: m.Subject, Notes: m.Body,
	}
}

// DocFromAccount builds a searchable email account document.
func DocFromAccount(a models.EmailAccount) Document {
	title := a.Email
	if title == "" {
		title = fmt.Sprintf("Account #%d", a.ID)
	}
	return Document{
		Kind: KindAccount, EntityID: a.ID, WorkspaceID: a.WorkspaceID, OwnerID: a.OwnerID,
		Title: title, Snippet: Snippet(JoinText(a.Provider, a.Domain), 160),
		Content: JoinText(a.Email, a.Provider, a.Domain, a.SMTPHost, a.IMAPHost), Href: "/accounts",
		Email: a.Email, Website: a.Domain, Company: a.Provider,
	}
}
