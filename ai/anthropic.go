package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type anthropicClient struct {
	apiKey string
	client *http.Client
}

func newAnthropicClient(apiKey string) *anthropicClient {
	return &anthropicClient{
		apiKey: apiKey,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *anthropicClient) Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	body := map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 4096,
		"system":     systemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": userPrompt},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return "", fmt.Errorf("Anthropic API error %d: %s", resp.StatusCode, apiErr.Error.Message)
		}
		return "", fmt.Errorf("Anthropic API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("no content in response")
	}
	return result.Content[0].Text, nil
}
