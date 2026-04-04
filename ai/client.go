package ai

import (
	"context"
	"fmt"
)

type Client interface {
	Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

func NewClient(cfg Config) (Client, error) {
	switch cfg.Provider {
	case "anthropic":
		if cfg.Anthropic == nil || cfg.Anthropic.APIKey == "" {
			return nil, fmt.Errorf("anthropic API key is required")
		}
		return newAnthropicClient(cfg.Anthropic.APIKey), nil
	case "openai":
		if cfg.OpenAI == nil || cfg.OpenAI.APIKey == "" {
			return nil, fmt.Errorf("openai API key is required")
		}
		return newOpenAIClient("https://api.openai.com/v1", cfg.OpenAI.APIKey, "gpt-4o"), nil
	case "openai_compatible":
		if cfg.OpenAICompatible == nil {
			return nil, fmt.Errorf("openai_compatible config is required")
		}
		return newOpenAIClient(cfg.OpenAICompatible.BaseURL, cfg.OpenAICompatible.APIKey, cfg.OpenAICompatible.Model), nil
	case "gemini":
		if cfg.Gemini == nil || cfg.Gemini.APIKey == "" {
			return nil, fmt.Errorf("gemini API key is required")
		}
		model := cfg.Gemini.Model
		if model == "" {
			model = "gemini-2.0-flash"
		}
		return newGeminiClient(cfg.Gemini.APIKey, model), nil
	default:
		return nil, fmt.Errorf("unknown or disabled provider: %q", cfg.Provider)
	}
}
