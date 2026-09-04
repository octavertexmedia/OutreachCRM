package search

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type pgEngine struct {
	db       *sql.DB
	embedder Embedder
}

func openPostgresEngine(databaseURL string, embedder Embedder) (Engine, error) {
	if embedder == nil {
		embedder = HashEmbedder{}
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres search: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres search ping: %w", err)
	}
	if _, err := db.ExecContext(ctx, pgSearchMigrateSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres search migrate: %w", err)
	}
	return &pgEngine{db: db, embedder: embedder}, nil
}

func (e *pgEngine) Backend() string {
	return "pgvector-hybrid(" + e.embedder.Name() + ")"
}

func (e *pgEngine) embedText(text string) []float32 {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vec, err := e.embedder.Embed(ctx, text)
	if err != nil || len(vec) != EmbeddingDim {
		vec, _ = HashEmbedder{}.Embed(ctx, text)
	}
	if len(vec) != EmbeddingDim {
		return zeroVec()
	}
	return vec
}

func (e *pgEngine) Upsert(docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT INTO search_docs (
		pk, kind, entity_id, workspace_id, owner_id, title, snippet, href, content,
		name, email, phone, website, company, subject, notes, facets, embedding)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::vector)
		ON CONFLICT (pk) DO UPDATE SET
			kind=EXCLUDED.kind, entity_id=EXCLUDED.entity_id, workspace_id=EXCLUDED.workspace_id,
			owner_id=EXCLUDED.owner_id, title=EXCLUDED.title, snippet=EXCLUDED.snippet,
			href=EXCLUDED.href, content=EXCLUDED.content,
			name=EXCLUDED.name, email=EXCLUDED.email, phone=EXCLUDED.phone,
			website=EXCLUDED.website, company=EXCLUDED.company, subject=EXCLUDED.subject,
			notes=EXCLUDED.notes, facets=EXCLUDED.facets, embedding=EXCLUDED.embedding`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, d := range docs {
		pk := PK(d.Kind, d.EntityID)
		emb := FormatVector(e.embedText(JoinText(d.Title, d.Content)))
		if _, err := stmt.Exec(pk, string(d.Kind), d.EntityID, d.WorkspaceID, d.OwnerID,
			d.Title, d.Snippet, d.Href, d.Content,
			d.Name, d.Email, d.Phone, d.Website, d.Company, d.Subject, d.Notes, FacetTags(d), emb); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (e *pgEngine) Delete(kind Kind, entityID int64) error {
	_, err := e.db.Exec(`DELETE FROM search_docs WHERE pk = $1`, PK(kind, entityID))
	return err
}

func (e *pgEngine) Clear() error {
	_, err := e.db.Exec(`DELETE FROM search_docs`)
	return err
}

func (e *pgEngine) Close() error {
	if e.db == nil {
		return nil
	}
	err := e.db.Close()
	e.db = nil
	return err
}

func (e *pgEngine) Search(q Query) ([]Result, error) {
	match := sanitizeMatch(q.Text)
	if match == "" {
		return nil, nil
	}
	limit := clampSearchLimit(q.Limit)
	cand := hybridCandidateLimit(limit)

	embSrc := match
	if q.Field != FieldAny {
		embSrc = q.Field + " " + match
	}
	qvec := FormatVector(e.embedText(embSrc))

	vecSQL, vecArgs := pgVectorSQL(q, qvec, cand)
	ftsSQL, ftsArgs := pgFTSSQL(q, match, cand)
	triSQL, triArgs := pgTrigramSQL(q, match, cand)

	vecHits, err := e.queryHits(vecSQL, vecArgs)
	if err != nil {
		return nil, fmt.Errorf("pgvector knn: %w", err)
	}
	ftsHits, err := e.queryHits(ftsSQL, ftsArgs)
	if err != nil {
		return nil, fmt.Errorf("postgres fts: %w", err)
	}
	triHits, err := e.queryHits(triSQL, triArgs)
	if err != nil {
		return nil, fmt.Errorf("postgres trgm: %w", err)
	}
	return ReciprocalRankFusion([][]Result{vecHits, ftsHits, triHits}, DefaultRRFK, limit), nil
}

func (e *pgEngine) queryHits(query string, args []any) ([]Result, error) {
	rows, err := e.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var r Result
		var kind string
		var score sql.NullFloat64
		if err := rows.Scan(&kind, &r.EntityID, &r.WorkspaceID, &r.OwnerID, &r.Title, &r.Snippet, &r.Href, &r.Facets, &score); err != nil {
			return nil, err
		}
		r.Kind = Kind(kind)
		if score.Valid {
			r.Score = score.Float64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
