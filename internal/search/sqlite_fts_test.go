//go:build !zvec

package search

import (
	"path/filepath"
	"testing"
)

func TestSQLiteFTSSearch(t *testing.T) {
	dir := t.TempDir()
	eng, err := openEngine(filepath.Join(dir, "search"), HashEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if eng.Backend() != "sqlite-fts5" {
		t.Fatalf("backend=%s", eng.Backend())
	}

	docs := []Document{
		{Kind: KindLead, EntityID: 1, WorkspaceID: 1, OwnerID: 2, Title: "Acme Robotics", Snippet: "ceo@acme.test", Content: "Acme Robotics ceo@acme.test manufacturing", Href: "/leads#lead-1"},
		{Kind: KindLead, EntityID: 2, WorkspaceID: 1, OwnerID: 3, Title: "Beta Foods", Snippet: "hello@beta.test", Content: "Beta Foods bakery", Href: "/leads#lead-2"},
		{Kind: KindCampaign, EntityID: 9, WorkspaceID: 1, OwnerID: 2, Title: "Q3 Outreach", Snippet: "active", Content: "Q3 Outreach manufacturing leads", Href: "/campaigns#campaign-9"},
		{Kind: KindLead, EntityID: 3, WorkspaceID: 2, OwnerID: 2, Title: "Other WS", Snippet: "x", Content: "Acme other workspace", Href: "/leads#lead-3"},
	}
	if err := eng.Upsert(docs); err != nil {
		t.Fatal(err)
	}

	hits, err := eng.Search(Query{Text: "acme", WorkspaceID: 1, Admin: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("admin ws1 acme: got %d hits %#v", len(hits), hits)
	}
	if hits[0].EntityID != 1 || hits[0].Kind != KindLead {
		t.Fatalf("unexpected hit: %#v", hits[0])
	}

	hits, err = eng.Search(Query{Text: "manufacturing", WorkspaceID: 1, OwnerID: 2, Admin: false, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 1 {
		t.Fatal("expected owner-scoped hits")
	}
	for _, h := range hits {
		if h.OwnerID != 0 && h.OwnerID != 2 {
			t.Fatalf("owner leak: %#v", h)
		}
	}

	if err := eng.Delete(KindLead, 1); err != nil {
		t.Fatal(err)
	}
	hits, err = eng.Search(Query{Text: "acme", WorkspaceID: 1, Admin: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(hits))
	}
}
