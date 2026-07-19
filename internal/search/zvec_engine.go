//go:build zvec

package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	zvec "github.com/zvec-ai/zvec-go"
)

// schemaVersion bumps when the on-disk collection layout changes.
const schemaVersion = "hybrid-hnsw-fts-1536-v1"

// zvecEngine is Alibaba Zvec with dense HNSW + native FTS + hybrid MultiQuery RRF.
type zvecEngine struct {
	mu       sync.Mutex
	col      *zvec.Collection
	path     string
	embedder Embedder
}

func openEngine(dataDir string, embedder Embedder) (Engine, error) {
	if embedder == nil {
		embedder = HashEmbedder{}
	}
	if err := zvec.Initialize(nil); err != nil {
		return nil, fmt.Errorf("zvec init: %w", err)
	}
	path := filepath.Join(dataDir, "zvec", "global")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	e := &zvecEngine{path: path, embedder: embedder}
	marker := filepath.Join(filepath.Dir(path), ".schema_version")
	if dirNonEmpty(path) {
		prev, _ := os.ReadFile(marker)
		if string(prev) != schemaVersion {
			_ = os.RemoveAll(path)
		} else if col, err := zvec.Open(path, nil); err == nil {
			e.col = col
			return e, nil
		} else {
			_ = os.RemoveAll(path)
		}
	}
	col, err := createZvecCollection(path)
	if err != nil {
		_ = zvec.Shutdown()
		return nil, err
	}
	_ = os.WriteFile(marker, []byte(schemaVersion), 0o644)
	e.col = col
	return e, nil
}

func dirNonEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}

func createZvecCollection(path string) (*zvec.Collection, error) {
	schema := zvec.NewCollectionSchema("outreach_global")
	defer schema.Destroy()

	addStringInvert := func(name string) error {
		f := zvec.NewFieldSchema(name, zvec.DataTypeString, false, 0)
		defer f.Destroy()
		inv, err := zvec.NewInvertIndexParams(true, false)
		if err != nil {
			return err
		}
		defer inv.Destroy()
		if err := f.SetIndexParams(inv); err != nil {
			return err
		}
		return schema.AddField(f)
	}
	addStringPlain := func(name string, nullable bool) error {
		f := zvec.NewFieldSchema(name, zvec.DataTypeString, nullable, 0)
		defer f.Destroy()
		return schema.AddField(f)
	}
	addInt64 := func(name string) error {
		f := zvec.NewFieldSchema(name, zvec.DataTypeInt64, false, 0)
		defer f.Destroy()
		inv, err := zvec.NewInvertIndexParams(true, false)
		if err != nil {
			return err
		}
		defer inv.Destroy()
		if err := f.SetIndexParams(inv); err != nil {
			return err
		}
		return schema.AddField(f)
	}

	if err := addStringInvert("id"); err != nil {
		return nil, err
	}
	if err := addStringPlain("kind", false); err != nil {
		return nil, err
	}
	if err := addInt64("entity_id"); err != nil {
		return nil, err
	}
	if err := addInt64("workspace_id"); err != nil {
		return nil, err
	}
	if err := addInt64("owner_id"); err != nil {
		return nil, err
	}
	if err := addStringPlain("title", false); err != nil {
		return nil, err
	}
	if err := addStringPlain("snippet", true); err != nil {
		return nil, err
	}
	if err := addStringPlain("href", true); err != nil {
		return nil, err
	}

	// Native FTS (standard tokenizer + lowercase)
	content := zvec.NewFieldSchema("content", zvec.DataTypeString, false, 0)
	defer content.Destroy()
	ftsParams, err := zvec.NewFTSIndexParams("standard", []string{"lowercase"}, "")
	if err != nil {
		return nil, err
	}
	defer ftsParams.Destroy()
	if err := content.SetIndexParams(ftsParams); err != nil {
		return nil, err
	}
	if err := schema.AddField(content); err != nil {
		return nil, err
	}

	// Dense vector + HNSW (cosine) for semantic / hybrid retrieval
	emb := zvec.NewFieldSchema("embedding", zvec.DataTypeVectorFP32, false, EmbeddingDim)
	defer emb.Destroy()
	hnsw, err := zvec.NewHNSWIndexParams(zvec.MetricTypeCosine, 16, 200)
	if err != nil {
		return nil, err
	}
	defer hnsw.Destroy()
	if err := emb.SetIndexParams(hnsw); err != nil {
		return nil, err
	}
	if err := schema.AddField(emb); err != nil {
		return nil, err
	}

	col, err := zvec.CreateAndOpen(path, schema, nil)
	if err != nil {
		return nil, fmt.Errorf("zvec create: %w", err)
	}
	return col, nil
}

func (e *zvecEngine) Backend() string {
	return "zvec-hybrid(" + e.embedder.Name() + ")"
}

func (e *zvecEngine) embedText(text string) []float32 {
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

func (e *zvecEngine) Upsert(docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	batch := make([]*zvec.Doc, 0, len(docs))
	for _, d := range docs {
		doc := zvec.NewDoc()
		pk := PK(d.Kind, d.EntityID)
		doc.SetPK(pk)
		_ = doc.AddStringField("id", pk)
		_ = doc.AddStringField("kind", string(d.Kind))
		_ = doc.AddInt64Field("entity_id", d.EntityID)
		_ = doc.AddInt64Field("workspace_id", d.WorkspaceID)
		_ = doc.AddInt64Field("owner_id", d.OwnerID)
		_ = doc.AddStringField("title", d.Title)
		_ = doc.AddStringField("snippet", d.Snippet)
		_ = doc.AddStringField("href", d.Href)
		_ = doc.AddStringField("content", d.Content)
		embText := JoinText(d.Title, d.Content)
		_ = doc.AddVectorFP32Field("embedding", e.embedText(embText))
		batch = append(batch, doc)
	}
	defer func() {
		for _, d := range batch {
			d.Destroy()
		}
	}()

	if _, err := e.col.Upsert(batch); err != nil {
		return err
	}
	return e.col.Flush()
}

func (e *zvecEngine) Delete(kind Kind, entityID int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.col.Delete([]string{PK(kind, entityID)})
	if err != nil {
		return err
	}
	return e.col.Flush()
}

func (e *zvecEngine) Clear() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.col != nil {
		_ = e.col.Destroy()
		e.col = nil
	}
	_ = os.RemoveAll(e.path)
	col, err := createZvecCollection(e.path)
	if err != nil {
		return err
	}
	marker := filepath.Join(filepath.Dir(e.path), ".schema_version")
	_ = os.WriteFile(marker, []byte(schemaVersion), 0o644)
	e.col = col
	return nil
}

func (e *zvecEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.col != nil {
		_ = e.col.Close()
		e.col = nil
	}
	return zvec.Shutdown()
}

func (e *zvecEngine) Search(q Query) ([]Result, error) {
	match := sanitizeMatch(q.Text)
	if match == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}

	filter := fmt.Sprintf("workspace_id = %d", q.WorkspaceID)
	if !q.Admin && q.OwnerID > 0 {
		filter += fmt.Sprintf(" AND (owner_id = 0 OR owner_id = %d)", q.OwnerID)
	}
	outFields := []string{"kind", "entity_id", "workspace_id", "owner_id", "title", "snippet", "href"}
	qvec := e.embedText(match)

	e.mu.Lock()
	defer e.mu.Unlock()

	docs, err := e.hybridQuery(match, qvec, filter, outFields, limit)
	if err != nil {
		// Fall back to FTS-only if MultiQuery path fails
		docs, err = e.ftsOnlyQuery(match, qvec, filter, outFields, limit)
		if err != nil {
			return nil, err
		}
	}
	defer zvec.FreeDocs(docs)
	return docsToResults(docs), nil
}

func (e *zvecEngine) hybridQuery(match string, qvec []float32, filter string, outFields []string, limit int) ([]*zvec.Doc, error) {
	mq := zvec.NewMultiQuery()
	if mq == nil {
		return nil, fmt.Errorf("zvec: NewMultiQuery failed")
	}
	defer mq.Destroy()

	_ = mq.SetTopK(limit)
	_ = mq.SetFilter(filter)
	_ = mq.SetIncludeVector(false)
	_ = mq.SetOutputFields(outFields)
	_ = mq.SetRerankRRF(60)

	candidates := limit * 4
	if candidates < 40 {
		candidates = 40
	}

	// Dense vector sub-query (HNSW / cosine)
	vecSub := zvec.NewSubQuery()
	if vecSub == nil {
		return nil, fmt.Errorf("zvec: NewSubQuery failed")
	}
	defer vecSub.Destroy()
	_ = vecSub.SetFieldName("embedding")
	_ = vecSub.SetNumCandidates(candidates)
	if err := vecSub.SetQueryVector(qvec); err != nil {
		return nil, err
	}
	hnswQ := zvec.NewHNSWQueryParams(64, 0, false, false)
	if hnswQ != nil {
		_ = vecSub.SetHNSWParams(hnswQ) // ownership transferred on success
	}
	if err := mq.AddSubQuery(vecSub); err != nil {
		return nil, err
	}

	// Native FTS sub-query
	ftsSub := zvec.NewSubQuery()
	if ftsSub == nil {
		return nil, fmt.Errorf("zvec: NewSubQuery(fts) failed")
	}
	defer ftsSub.Destroy()
	_ = ftsSub.SetFieldName("content")
	_ = ftsSub.SetNumCandidates(candidates)
	fts := zvec.NewFTS()
	if fts == nil {
		return nil, fmt.Errorf("zvec: NewFTS failed")
	}
	defer fts.Destroy()
	if err := fts.SetMatchString(match); err != nil {
		return nil, err
	}
	if err := ftsSub.SetFTS(fts); err != nil {
		return nil, err
	}
	ftsParams := zvec.NewFTSQueryParams("OR")
	if ftsParams != nil {
		_ = ftsSub.SetFTSParams(ftsParams) // ownership transferred
	}
	if err := mq.AddSubQuery(ftsSub); err != nil {
		return nil, err
	}

	return e.col.MultiQuery(mq)
}

func (e *zvecEngine) ftsOnlyQuery(match string, qvec []float32, filter string, outFields []string, limit int) ([]*zvec.Doc, error) {
	query := zvec.NewSearchQuery()
	defer query.Destroy()
	_ = query.SetFieldName("embedding")
	_ = query.SetTopK(limit)
	_ = query.SetQueryVector(qvec)
	_ = query.SetIncludeVector(false)
	_ = query.SetOutputFields(outFields)
	_ = query.SetFilter(filter)
	fts := zvec.NewFTS()
	if fts == nil {
		return nil, fmt.Errorf("zvec: NewFTS failed")
	}
	defer fts.Destroy()
	if err := fts.SetMatchString(match); err != nil {
		return nil, err
	}
	if err := query.SetFTS(fts); err != nil {
		return nil, err
	}
	return e.col.Query(query)
}

func docsToResults(docs []*zvec.Doc) []Result {
	out := make([]Result, 0, len(docs))
	for _, d := range docs {
		kind, _ := d.GetStringField("kind")
		title, _ := d.GetStringField("title")
		snippet, _ := d.GetStringField("snippet")
		href, _ := d.GetStringField("href")
		entityID, _ := d.GetInt64Field("entity_id")
		ws, _ := d.GetInt64Field("workspace_id")
		owner, _ := d.GetInt64Field("owner_id")
		out = append(out, Result{
			Kind:        Kind(kind),
			EntityID:    entityID,
			WorkspaceID: ws,
			OwnerID:     owner,
			Title:       title,
			Snippet:     snippet,
			Href:        href,
			Score:       float64(d.GetScore()),
		})
	}
	return out
}
