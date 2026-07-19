package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// EmbeddingDim is the dense vector size stored in Zvec (text-embedding-3-small).
const EmbeddingDim = 1536

// Embedder produces dense vectors for hybrid search.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Name() string
}

// OpenAIEmbedder calls the OpenAI-compatible /embeddings API.
type OpenAIEmbedder struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

// NewOpenAIEmbedder builds an embedder. Empty apiKey → nil (caller should use HashEmbedder).
func NewOpenAIEmbedder(apiKey, baseURL, model string) *OpenAIEmbedder {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &OpenAIEmbedder{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		HTTP:    &http.Client{Timeout: 45 * time.Second},
	}
}

func (e *OpenAIEmbedder) Name() string { return "openai:" + e.Model }

func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return zeroVec(), nil
	}
	if len(text) > 8000 {
		text = text[:8000]
	}
	body, err := json.Marshal(map[string]any{
		"model": e.Model,
		"input": text,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)
	res, err := e.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("embeddings http %d: %s", res.StatusCode, truncate(string(raw), 200))
	}
	var parsed struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("embeddings: %s", parsed.Error.Message)
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding")
	}
	src := parsed.Data[0].Embedding
	out := make([]float32, EmbeddingDim)
	n := len(src)
	if n > EmbeddingDim {
		n = EmbeddingDim
	}
	for i := 0; i < n; i++ {
		out[i] = float32(src[i])
	}
	return l2norm(out), nil
}

// HashEmbedder is a local deterministic feature-hash embedder used when no API key
// is configured, so hybrid dense+FTS still works offline.
type HashEmbedder struct{}

func (HashEmbedder) Name() string { return "hash" }

func (HashEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	return hashEmbed(text), nil
}

// ResolveEmbedder prefers OpenAI, else hash fallback.
func ResolveEmbedder(apiKey, baseURL, model string) Embedder {
	if e := NewOpenAIEmbedder(apiKey, baseURL, model); e != nil {
		return e
	}
	return HashEmbedder{}
}

func hashEmbed(text string) []float32 {
	out := make([]float32, EmbeddingDim)
	toks := strings.Fields(strings.ToLower(text))
	if len(toks) == 0 {
		return out
	}
	for _, tok := range toks {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tok))
		v := h.Sum64()
		i := int(v % uint64(EmbeddingDim))
		sign := float32(1)
		if v&1 == 1 {
			sign = -1
		}
		out[i] += sign
		// second hash for denser coverage
		h2 := fnv.New64a()
		_, _ = h2.Write([]byte(tok + "#2"))
		v2 := h2.Sum64()
		j := int(v2 % uint64(EmbeddingDim))
		sign2 := float32(1)
		if v2&1 == 1 {
			sign2 = -1
		}
		out[j] += 0.5 * sign2
	}
	return l2norm(out)
}

func zeroVec() []float32 { return make([]float32, EmbeddingDim) }

func l2norm(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum < 1e-12 {
		return v
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
