package search

import (
	"os"
	"testing"
)

func TestPostgresEngineIfAvailable(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://outreach:outreach@127.0.0.1:5432/outreachcrm?sslmode=disable"
	}
	if !PostgresSearchURL(url) {
		t.Skip("no postgres URL")
	}
	eng, err := openPostgresEngine(url, HashEmbedder{})
	if err != nil {
		t.Skip(err)
	}
	defer eng.Close()
	if got := eng.Backend(); len(got) < 8 || got[:8] != "pgvector" {
		t.Fatalf("backend=%s", eng.Backend())
	}

	docs := []Document{
		{Kind: KindLead, EntityID: 91001, WorkspaceID: 91001, OwnerID: 2, Title: "Acme Robotics", Snippet: "ceo@acme.test", Content: "Acme Robotics manufacturing automation CNC", Href: "/leads", Name: "Ada", Email: "ceo@acme.test", Company: "Acme"},
		{Kind: KindCampaign, EntityID: 91009, WorkspaceID: 91001, OwnerID: 2, Title: "Q3 Outreach", Snippet: "active", Content: "Q3 Outreach manufacturing leads sequence", Href: "/campaigns", Name: "Q3 Outreach"},
		{Kind: KindLead, EntityID: 91003, WorkspaceID: 91002, OwnerID: 2, Title: "Other WS", Snippet: "x", Content: "Acme other workspace bakery", Href: "/leads", Email: "x@other.test"},
	}
	if err := eng.Upsert(docs); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = eng.Delete(KindLead, 91001)
		_ = eng.Delete(KindCampaign, 91009)
		_ = eng.Delete(KindLead, 91003)
	})

	hits, err := eng.Search(Query{Text: "acme", WorkspaceID: 91001, Admin: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 1 {
		t.Fatalf("expected acme hits, got %#v", hits)
	}
	found := false
	for _, h := range hits {
		if h.WorkspaceID != 91001 {
			t.Fatalf("workspace leak: %#v", h)
		}
		if h.EntityID == 91001 && h.Kind == KindLead {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing lead 91001 in %#v", hits)
	}
}
