package search

import (
	"strings"
	"testing"
)

func TestReciprocalRankFusion(t *testing.T) {
	vec := []Result{
		{Kind: KindLead, EntityID: 1, Title: "vec-first"},
		{Kind: KindLead, EntityID: 2, Title: "vec-second"},
		{Kind: KindCampaign, EntityID: 9, Title: "vec-third"},
	}
	fts := []Result{
		{Kind: KindLead, EntityID: 2, Title: "fts-first"},
		{Kind: KindLead, EntityID: 3, Title: "fts-second"},
		{Kind: KindLead, EntityID: 1, Title: "fts-third"},
	}
	out := ReciprocalRankFusion([][]Result{vec, fts}, 60, 10)
	if len(out) < 3 {
		t.Fatalf("got %d hits", len(out))
	}
	// ID 2 ranks in both lists near the top → highest RRF.
	if out[0].EntityID != 2 || out[0].Kind != KindLead {
		t.Fatalf("expected lead 2 first, got %#v", out[0])
	}
	if out[0].Score <= out[1].Score {
		t.Fatalf("expected fused score to outrank singles: %#v", out[:2])
	}
}

func TestFormatVector(t *testing.T) {
	s := FormatVector([]float32{0.5, -1, 0})
	if s != "[0.5,-1,0]" {
		t.Fatalf("got %q", s)
	}
	z := FormatVector(nil)
	if !strings.HasPrefix(z, "[") || !strings.HasSuffix(z, "]") {
		t.Fatalf("zero vec %q", z)
	}
	if strings.Count(z, ",") != EmbeddingDim-1 {
		t.Fatalf("expected %d commas", EmbeddingDim-1)
	}
}

func TestPostgresSearchURL(t *testing.T) {
	if !PostgresSearchURL("postgres://u:p@localhost:5432/db") {
		t.Fatal("postgres scheme")
	}
	if !PostgresSearchURL("postgresql://localhost/db") {
		t.Fatal("postgresql scheme")
	}
	if PostgresSearchURL("") || PostgresSearchURL("sqlite:///tmp/x") {
		t.Fatal("non-postgres should be false")
	}
}

func TestPGVectorSQLPlaceholders(t *testing.T) {
	q := Query{Text: "acme", WorkspaceID: 1, Admin: true, Kind: KindLead, Field: FieldEmail, Limit: 10}
	sql, args := pgVectorSQL(q, "[0,1]", 40)
	if !strings.Contains(sql, "embedding <=> $") {
		t.Fatalf("missing knn: %s", sql)
	}
	if !strings.Contains(sql, "::vector") {
		t.Fatal("missing vector cast")
	}
	if !strings.Contains(sql, "ORDER BY embedding <=>") {
		t.Fatal("missing order")
	}
	if len(args) < 4 {
		t.Fatalf("args=%v", args)
	}
	if args[0] != int64(1) && args[0] != 1 {
		t.Fatalf("workspace arg %#v", args[0])
	}
}

func TestPGFTSSQL(t *testing.T) {
	q := Query{WorkspaceID: 7, Admin: false, OwnerID: 3}
	sql, args := pgFTSSQL(q, "robotics demo", 40)
	if !strings.Contains(sql, "plainto_tsquery") || !strings.Contains(sql, "ts_rank_cd") {
		t.Fatalf("fts sql: %s", sql)
	}
	if !strings.Contains(sql, "content_tsv @@ query") {
		t.Fatal("missing @@")
	}
	if len(args) != 4 { // ws, owner, query text, limit
		t.Fatalf("args len %d %#v", len(args), args)
	}
}

func TestPGTrigramSQLField(t *testing.T) {
	q := Query{WorkspaceID: 1, Admin: true, Field: FieldEmail}
	sql, args := pgTrigramSQL(q, "acme.test", 40)
	if !strings.Contains(sql, "email ILIKE") {
		t.Fatalf("expected email column: %s", sql)
	}
	if strings.Contains(sql, "name ILIKE") {
		t.Fatal("field-scoped should not search name")
	}
	if len(args) != 5 {
		t.Fatalf("args=%#v", args)
	}
}

func TestPGScopeKindEmail(t *testing.T) {
	a := &pgArgs{}
	q := Query{WorkspaceID: 1, Admin: true, Kind: KindEmail}
	clause := pgScopeSQL(q, a)
	if !strings.Contains(clause, "kind IN ('lead','account','reply','queue')") {
		t.Fatalf("clause=%s", clause)
	}
}
