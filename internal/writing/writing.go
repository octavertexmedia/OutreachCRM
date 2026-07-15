package writing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/llm"
	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/spintax"
)

type Draft struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type Service struct {
	LLM *llm.Client
}

func (s *Service) Generate(ctx context.Context, lead models.Lead) (Draft, error) {
	if s.LLM == nil || !s.LLM.Enabled() {
		return heuristicDraft(lead), nil
	}
	system := `You write cold outreach using the Hyper-Personalization Formula:
1) Mention ONE specific problem from issues/signals (e.g. weak Google reviews, outdated site)
2) Name the business consequence (losing leads / credibility / wasted ad spend)
3) Present a simple fix
4) End with low-friction CTA ("Open to seeing a quick audit?")
Requirements:
- Use Spintax {option1|option2} for 2-4 swaps in subject AND body
- Keep under 120 words
- Sound human, not salesy
Return JSON only: {"subject":"...","body":"..."}`
	user := fmt.Sprintf(`Name: %s
Company: %s
Title: %s
Website: %s
Category: %s
Issues: %s
Premium score: %d
Confidence: %d
Notes: %s`, lead.Name, lead.Company, lead.Title, lead.Website, lead.Category, lead.IssuesJSON, lead.PremiumScore, lead.Confidence, lead.Notes)

	raw, err := s.LLM.Chat(ctx, system, user, true)
	if err != nil {
		return Draft{}, err
	}
	var d Draft
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return Draft{}, err
	}
	if d.Subject == "" || d.Body == "" {
		return heuristicDraft(lead), nil
	}
	return d, nil
}

// SuggestReply drafts a short human follow-up for a positive/neutral inbound reply.
func (s *Service) SuggestReply(ctx context.Context, lead models.Lead, inbound string) (string, error) {
	if s.LLM == nil || !s.LLM.Enabled() {
		name := lead.Name
		if name == "" {
			name = "there"
		}
		return fmt.Sprintf("Thanks %s — glad this resonated. Would a 15-minute walkthrough this week work, or prefer async notes first?", name), nil
	}
	system := `Write a short, warm sales reply (max 80 words) to a positive inbound. Propose one concrete next step. No spintax. Plain text only.`
	user := fmt.Sprintf("Lead: %s (%s)\nTheir message:\n%s", lead.Name, lead.Company, inbound)
	return s.LLM.Chat(ctx, system, user, false)
}

func Expand(subject, body string) (string, string) {
	return spintax.Expand(subject), spintax.Expand(body)
}

func Personalize(subject, body, name string) (string, string) {
	if name == "" {
		name = "there"
	}
	subject = strings.ReplaceAll(subject, "{{name}}", name)
	body = strings.ReplaceAll(body, "{{name}}", name)
	return Expand(subject, body)
}

func heuristicDraft(lead models.Lead) Draft {
	issue := "your online presence"
	if lead.IssuesJSON != "" && lead.IssuesJSON != "[]" {
		var issues []string
		_ = json.Unmarshal([]byte(lead.IssuesJSON), &issues)
		if len(issues) > 0 {
			issue = issues[0]
		}
	}
	name := lead.Name
	if name == "" {
		name = "there"
	}
	company := lead.Company
	if company == "" {
		company = name
	}
	return Draft{
		Subject: fmt.Sprintf("{Quick thought|Idea|Notice} for %s", company),
		Body: strings.TrimSpace(fmt.Sprintf(`Hi %s,

{I noticed|Came across} %s at %s — that often means {you're losing leads|prospects bounce} before they reach out.

{We help teams|Our fix is} a simple audit + {fast cleanup|tight landing page} so the right people convert.

{Open to seeing a quick audit?|Want a 2-minute teardown?}

{Best|Thanks},
{Alex|Sam}`, name, issue, company)),
	}
}
