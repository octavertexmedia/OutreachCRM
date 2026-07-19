package search

import (
	"context"
	"testing"
)

func TestHashEmbedderDimAndNorm(t *testing.T) {
	v, err := HashEmbedder{}.Embed(context.Background(), "Acme Robotics manufacturing")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != EmbeddingDim {
		t.Fatalf("dim=%d want %d", len(v), EmbeddingDim)
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum < 0.9 || sum > 1.1 {
		t.Fatalf("expected ~unit norm, got %f", sum)
	}
}

func TestResolveEmbedderFallback(t *testing.T) {
	e := ResolveEmbedder("", "https://api.openai.com/v1", "")
	if e.Name() != "hash" {
		t.Fatalf("got %s", e.Name())
	}
	e2 := ResolveEmbedder("sk-test", "https://api.openai.com/v1", "text-embedding-3-small")
	if e2.Name() != "openai:text-embedding-3-small" {
		t.Fatalf("got %s", e2.Name())
	}
}
