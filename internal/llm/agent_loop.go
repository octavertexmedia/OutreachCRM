package llm

import (
	"context"
	"fmt"
	"strings"
)

type ToolTrace struct {
	Name   string `json:"name"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type AgentLoopResult struct {
	Answer string
	Tools  []ToolTrace
	Rounds int
}

const maxToolRounds = 4

func RunToolAgent(ctx context.Context, client *Client, messages []Message, exec *ToolExecutor) (*AgentLoopResult, error) {
	if client == nil {
		return nil, fmt.Errorf("LLM client required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tools := AllAgentTools()
	out := &AgentLoopResult{}
	msgs := append([]Message{}, messages...)

	for round := 0; round < maxToolRounds; round++ {
		turn, err := client.ChatTurn(ctx, msgs, tools, false)
		if err != nil {
			if round == 0 && looksLikeToolsUnsupported(err) {
				answer, err2 := client.ChatTurn(ctx, msgs, nil, false)
				if err2 != nil {
					return nil, err
				}
				out.Answer = strings.TrimSpace(answer.Message.Content)
				out.Rounds = 1
				if out.Answer == "" {
					return nil, fmt.Errorf("LLM returned an empty answer")
				}
				return out, nil
			}
			return nil, err
		}
		out.Rounds++
		msg := turn.Message
		if len(msg.ToolCalls) == 0 {
			out.Answer = strings.TrimSpace(msg.Content)
			if out.Answer == "" {
				return nil, fmt.Errorf("LLM returned an empty answer")
			}
			return out, nil
		}
		msgs = append(msgs, Message{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: msg.ToolCalls,
		})
		for _, tc := range msg.ToolCalls {
			name := strings.TrimSpace(tc.Function.Name)
			args := tc.Function.Arguments
			trace := ToolTrace{Name: name, Args: truncateStr(args, 400)}
			result, err := exec.Execute(name, args)
			if err != nil {
				trace.Error = err.Error()
				result = toolErr(err.Error())
			} else {
				trace.Result = truncateStr(result, 800)
			}
			out.Tools = append(out.Tools, trace)
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       name,
				Content:    truncateRunes(result, 2400),
			})
		}
	}

	final, err := client.ChatTurn(ctx, append(msgs, Message{
		Role:    "user",
		Content: "Using the tool results above, give the final actionable answer now. Do not call more tools.",
	}), nil, false)
	if err != nil {
		return nil, err
	}
	out.Answer = strings.TrimSpace(final.Message.Content)
	return out, nil
}

func looksLikeToolsUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "tool") && (strings.Contains(s, "not support") ||
		strings.Contains(s, "unsupported") || strings.Contains(s, "unknown") ||
		strings.Contains(s, "invalid") || strings.Contains(s, "400"))
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
