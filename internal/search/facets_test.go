package search

import "testing"

func TestKindMatchesEmail(t *testing.T) {
	if !KindMatches(KindEmail, KindLead) || !KindMatches(KindEmail, KindReply) {
		t.Fatal("email category should include lead/reply")
	}
	if KindMatches(KindEmail, KindCampaign) {
		t.Fatal("email category should exclude campaign")
	}
	if !KindMatches("", KindCampaign) {
		t.Fatal("all should match")
	}
}

func TestFacetTags(t *testing.T) {
	d := Document{Name: "Ada", Email: "a@b.c", Phone: "1", Website: "https://x.test"}
	tags := FacetTags(d)
	for _, want := range []string{"has_name", "has_email", "has_phone", "has_website"} {
		if !containsWord(tags, want) {
			t.Fatalf("missing %s in %q", want, tags)
		}
	}
}

func containsWord(s, w string) bool {
	for _, p := range splitFields(s) {
		if p == w {
			return true
		}
	}
	return false
}

func splitFields(s string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	return out
}
