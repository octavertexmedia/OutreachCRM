package writing

import (
	"strings"
	"testing"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func TestPersonalizeLead_MergesCompanyWebsite(t *testing.T) {
	subj, body := PersonalizeLead(
		"{{company}} catalogue → quote flow",
		"Hi {{name}}, looked at {{website}} ({{title}}).",
		models.Lead{
			Name: "Suresh Patil", Company: "PackRight", Website: "https://packright.example.com", Title: "Owner",
		},
	)
	if subj != "PackRight catalogue → quote flow" {
		t.Fatalf("subject: %q", subj)
	}
	if !strings.Contains(body, "Hi Suresh Patil") || !strings.Contains(body, "https://packright.example.com") || !strings.Contains(body, "Owner") {
		t.Fatalf("body: %q", body)
	}
}

func TestPersonalizeLead_Fallbacks(t *testing.T) {
	subj, body := PersonalizeLead("{{company}} · {{website}}", "Hi {{name}}", models.Lead{})
	if subj != "there · your site" {
		t.Fatalf("subject: %q", subj)
	}
	if body != "Hi there" {
		t.Fatalf("body: %q", body)
	}
}
