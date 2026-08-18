package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	APIKey    string
	BaseURL   string
	Model     string
	MaxTokens int
	HTTP      *http.Client
}

func New(apiKey, baseURL, model string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &Client{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) Enabled() bool { return c != nil && c.APIKey != "" }

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type chatReq struct {
	Model          string    `json:"model"`
	Messages       []Message `json:"messages"`
	Temperature    float64   `json:"temperature"`
	MaxTokens      int       `json:"max_tokens,omitempty"`
	ResponseFormat *fmtType  `json:"response_format,omitempty"`
	Tools          []ToolDef `json:"tools,omitempty"`
	ToolChoice     string    `json:"tool_choice,omitempty"`
}

type fmtType struct {
	Type string `json:"type"`
}

type chatResp struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type ChatResult struct {
	Message      Message
	FinishReason string
}

func (c *Client) chatURL() string {
	if strings.HasSuffix(c.BaseURL, "/chat/completions") {
		return c.BaseURL
	}
	return c.BaseURL + "/chat/completions"
}

// Chat sends a single system+user turn (no tools). Used by enrichment, writing, classify.
func (c *Client) Chat(ctx context.Context, system, user string, jsonMode bool) (string, error) {
	msgs := []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}
	res, err := c.ChatTurn(ctx, msgs, nil, jsonMode)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Message.Content) == "" {
		return "", fmt.Errorf("empty llm response")
	}
	return strings.TrimSpace(res.Message.Content), nil
}

// ChatTurn sends one completion request, optionally with tools.
func (c *Client) ChatTurn(ctx context.Context, messages []Message, tools []ToolDef, jsonMode bool) (*ChatResult, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("openai api key not configured")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	maxTok := c.MaxTokens
	if maxTok <= 0 {
		maxTok = 1400
	}
	reqBody := chatReq{
		Model:       c.Model,
		Messages:    messages,
		Temperature: 0.4,
		MaxTokens:   maxTok,
	}
	if jsonMode {
		reqBody.ResponseFormat = &fmtType{Type: "json_object"}
	}
	if len(tools) > 0 {
		reqBody.Tools = tools
		reqBody.ToolChoice = "auto"
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL(), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var parsed chatResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode llm response: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("llm error: %s", parsed.Error.Message)
	}
	if res.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 240 {
			snippet = snippet[:240] + "…"
		}
		return nil, fmt.Errorf("llm http %d: %s", res.StatusCode, snippet)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("empty llm response")
	}
	msg := parsed.Choices[0].Message
	if msg.Role == "" {
		msg.Role = "assistant"
	}
	return &ChatResult{Message: msg, FinishReason: parsed.Choices[0].FinishReason}, nil
}
