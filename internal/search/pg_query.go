package search

import (
	"fmt"
	"strconv"
	"strings"
)

type pgArgs struct {
	list []any
}

func (a *pgArgs) add(v any) string {
	a.list = append(a.list, v)
	return fmt.Sprintf("$%d", len(a.list))
}

func clampSearchLimit(n int) int {
	if n <= 0 {
		return 25
	}
	if n > 100 {
		return 100
	}
	return n
}

func hybridCandidateLimit(limit int) int {
	c := limit * 4
	if c < 40 {
		c = 40
	}
	return c
}

func pgScopeSQL(q Query, a *pgArgs) string {
	var b strings.Builder
	b.WriteString("workspace_id = ")
	b.WriteString(a.add(q.WorkspaceID))
	if !q.Admin && q.OwnerID > 0 {
		b.WriteString(" AND (owner_id = 0 OR owner_id = ")
		b.WriteString(a.add(q.OwnerID))
		b.WriteString(")")
	}
	switch q.Kind {
	case "":
	case KindEmail:
		b.WriteString(" AND kind IN ('lead','account','reply','queue')")
	default:
		b.WriteString(" AND kind = ")
		b.WriteString(a.add(string(q.Kind)))
	}
	if q.Field != FieldAny {
		b.WriteString(" AND facets LIKE ")
		b.WriteString(a.add("%has_" + q.Field + "%"))
	}
	return b.String()
}

func pgSelectCols() string {
	return `kind, entity_id, workspace_id, owner_id, title, snippet, href, facets`
}

func pgVectorSQL(q Query, vec string, cand int) (string, []any) {
	a := &pgArgs{}
	scope := pgScopeSQL(q, a)
	vecP := a.add(vec)
	lim := a.add(cand)
	sql := fmt.Sprintf(`SELECT %s, (1 - (embedding <=> %s::vector)) AS score
FROM search_docs
WHERE %s AND embedding IS NOT NULL
ORDER BY embedding <=> %s::vector
LIMIT %s`, pgSelectCols(), vecP, scope, vecP, lim)
	return sql, a.list
}

func pgFTSSQL(q Query, match string, cand int) (string, []any) {
	a := &pgArgs{}
	scope := pgScopeSQL(q, a)
	qP := a.add(match)
	lim := a.add(cand)
	sql := fmt.Sprintf(`SELECT %s, ts_rank_cd(content_tsv, query) AS score
FROM search_docs, plainto_tsquery('english', %s) AS query
WHERE %s AND content_tsv @@ query
ORDER BY score DESC
LIMIT %s`, pgSelectCols(), qP, scope, lim)
	return sql, a.list
}

func pgTrigramSQL(q Query, match string, cand int) (string, []any) {
	a := &pgArgs{}
	scope := pgScopeSQL(q, a)
	like := "%" + strings.ToLower(match) + "%"
	likeP := a.add(like)
	rawP := a.add(match)
	lim := a.add(cand)

	col := q.Field
	var pred, scoreExpr string
	switch col {
	case FieldEmail, FieldName, FieldCompany, FieldPhone, FieldWebsite, FieldSubject, FieldNotes:
		pred = fmt.Sprintf("(%s ILIKE %s OR %s %% %s)", col, likeP, col, rawP)
		scoreExpr = fmt.Sprintf("similarity(%s, %s)", col, rawP)
	default:
		pred = fmt.Sprintf(`(email ILIKE %s OR name ILIKE %s OR company ILIKE %s
			OR email %% %s OR name %% %s OR company %% %s)`, likeP, likeP, likeP, rawP, rawP, rawP)
		scoreExpr = fmt.Sprintf("GREATEST(similarity(email, %s), similarity(name, %s), similarity(company, %s))", rawP, rawP, rawP)
	}

	sql := fmt.Sprintf(`SELECT %s, %s AS score
FROM search_docs
WHERE %s AND %s
ORDER BY score DESC
LIMIT %s`, pgSelectCols(), scoreExpr, scope, pred, lim)
	return sql, a.list
}

// FormatVector encodes a dense vector for pgvector's text input.
func FormatVector(v []float32) string {
	if len(v) == 0 {
		v = zeroVec()
	}
	var b strings.Builder
	b.Grow(len(v) * 8)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// PostgresSearchURL reports whether DATABASE_URL should enable the pgvector engine.
func PostgresSearchURL(databaseURL string) bool {
	u := strings.ToLower(strings.TrimSpace(databaseURL))
	return strings.HasPrefix(u, "postgres://") || strings.HasPrefix(u, "postgresql://")
}
