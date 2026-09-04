package search

import "sort"

// DefaultRRFK matches Zvec MultiQuery SetRerankRRF(60).
const DefaultRRFK = 60

// ReciprocalRankFusion merges ranked lists. Score = Σ 1/(k+rank) with rank starting at 1.
func ReciprocalRankFusion(lists [][]Result, k, limit int) []Result {
	if k <= 0 {
		k = DefaultRRFK
	}
	if limit <= 0 {
		limit = 25
	}
	type acc struct {
		hit   Result
		score float64
	}
	byPK := make(map[string]*acc, 64)
	for _, list := range lists {
		for i, hit := range list {
			pk := PK(hit.Kind, hit.EntityID)
			add := 1.0 / (float64(k) + float64(i+1))
			if a, ok := byPK[pk]; ok {
				a.score += add
				continue
			}
			h := hit
			byPK[pk] = &acc{hit: h, score: add}
		}
	}
	out := make([]Result, 0, len(byPK))
	for _, a := range byPK {
		a.hit.Score = a.score
		out = append(out, a.hit)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			if out[i].Kind == out[j].Kind {
				return out[i].EntityID < out[j].EntityID
			}
			return out[i].Kind < out[j].Kind
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
