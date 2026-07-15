package spintax

import (
	"math/rand"
	"strings"
	"time"
)

var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

// Expand replaces nested {a|b|c} spintax with one random choice per group.
func Expand(s string) string {
	const maxPasses = 32
	for i := 0; i < maxPasses; i++ {
		start := strings.LastIndex(s, "{")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			break
		}
		end += start
		inner := s[start+1 : end]
		opts := strings.Split(inner, "|")
		pick := opts[0]
		if len(opts) > 1 {
			pick = opts[rng.Intn(len(opts))]
		}
		s = s[:start] + pick + s[end+1:]
	}
	return s
}
