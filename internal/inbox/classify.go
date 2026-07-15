package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/llm"
)

type Service struct {
	LLM *llm.Client
}

type Result struct {
	Intent string `json:"intent"` // positive | neutral | unsubscribe | other
}

func (s *Service) Classify(ctx context.Context, body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "other", nil
	}
	if s.LLM == nil || !s.LLM.Enabled() {
		return heuristic(body), nil
	}
	system := `Classify inbound sales reply intent. Return JSON: {"intent":"positive|neutral|unsubscribe|other"}
positive = interested / wants call / open to audit
unsubscribe = stop / remove / not interested forever
neutral = polite maybe later / unclear
other = auto-reply / OOO / unrelated`
	raw, err := s.LLM.Chat(ctx, system, body, true)
	if err != nil {
		return "", err
	}
	var r Result
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return "", fmt.Errorf("parse intent: %w", err)
	}
	switch r.Intent {
	case "positive", "neutral", "unsubscribe", "other":
		return r.Intent, nil
	default:
		return "other", nil
	}
}

func heuristic(body string) string {
	l := strings.ToLower(body)
	switch {
	case strings.Contains(l, "unsubscribe") || strings.Contains(l, "remove me") || strings.Contains(l, "stop emailing"):
		return "unsubscribe"
	case strings.Contains(l, "interested") || strings.Contains(l, "let's talk") || strings.Contains(l, "lets talk") ||
		strings.Contains(l, "schedule") || strings.Contains(l, "book a") || strings.Contains(l, "open to"):
		return "positive"
	case strings.Contains(l, "maybe") || strings.Contains(l, "not right now") || strings.Contains(l, "later"):
		return "neutral"
	default:
		return "other"
	}
}
