//go:build zvec

package search

import (
	"path/filepath"
	"testing"
)

func TestZvecHybridSearch(t *testing.T) {
	dir := t.TempDir()
	eng, err := openEngine(filepath.Join(dir, "search"), HashEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if eng.Backend() == "" || eng.Backend()[:4] != "zvec" {
		t.Fatalf("backend=%s", eng.Backend())
	}

	docs := []Document{
		{Kind: KindLead, EntityID: 1, WorkspaceID: 1, OwnerID: 2, Title: "Acme Robotics", Snippet: "ceo@acme.test", Content: "Acme Robotics manufacturing automation CNC", Href: "/leads"},
		{Kind: KindCampaign, EntityID: 9, WorkspaceID: 1, OwnerID: 2, Title: "Q3 Outreach", Snippet: "active", Content: "Q3 Outreach manufacturing leads sequence", Href: "/campaigns"},
		{Kind: KindLead, EntityID: 3, WorkspaceID: 2, OwnerID: 2, Title: "Other WS", Snippet: "x", Content: "Acme other workspace bakery", Href: "/leads"},
		{Kind: KindReply, EntityID: 4, WorkspaceID: 1, OwnerID: 2, Title: "Re interested", Snippet: "positive", Content: "We are interested in a demo of your robotics platform", Href: "/hitl"},
	}
	if err := eng.Upsert(docs); err != nil {
		t.Fatal(err)
	}

	// Keyword / FTS path
	hits, err := eng.Search(Query{Text: "acme", WorkspaceID: 1, Admin: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 1 {
		t.Fatalf("expected acme hits, got %#v", hits)
	}
	foundAcme := false
	for _, h := range hits {
		if h.EntityID == 1 && h.Kind == KindLead {
			foundAcme = true
		}
		if h.WorkspaceID != 1 {
			t.Fatalf("workspace leak: %#v", h)
		}
	}
	if !foundAcme {
		t.Fatalf("missing lead 1 in %#v", hits)
	}

	// Semantic-ish query (hash embedder still ranks related manufacturing text)
	hits, err = eng.Search(Query{Text: "manufacturing automation", WorkspaceID: 1, Admin: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 1 {
		t.Fatal("expected hybrid manufacturing hits")
	}
}
