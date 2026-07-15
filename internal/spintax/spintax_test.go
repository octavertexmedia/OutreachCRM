package spintax

import (
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	out := Expand("Hello {Alice|Bob}")
	if out != "Hello Alice" && out != "Hello Bob" {
		t.Fatalf("unexpected: %q", out)
	}
	out = Expand("no braces")
	if out != "no braces" {
		t.Fatalf("unexpected: %q", out)
	}
	out = Expand("{a|{b|c}}")
	if !strings.Contains("abc", out) || len(out) == 0 {
		t.Fatalf("unexpected nested: %q", out)
	}
}
