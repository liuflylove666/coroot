package ai

import (
	"errors"

	"github.com/coroot/coroot/db"
)

const SettingName = "ai_settings"

type Config struct {
	Readonly         bool                    `json:"readonly,omitempty"`
	Provider         string                  `json:"provider"`
	Anthropic        *AnthropicConfig        `json:"anthropic,omitempty"`
	OpenAI           *OpenAIConfig           `json:"openai,omitempty"`
	OpenAICompatible *OpenAICompatibleConfig `json:"openai_compatible,omitempty"`
	Gemini           *GeminiConfig           `json:"gemini,omitempty"`
}

type AnthropicConfig struct {
	APIKey string `json:"api_key"`
}

type OpenAIConfig struct {
	APIKey string `json:"api_key"`
}

type OpenAICompatibleConfig struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

type GeminiConfig struct {
	APIKey string `json:"api_key"`
	Model  string `json:"model"`
}

func (c *Config) IsEnabled() bool {
	return c.Provider != ""
}

func GetConfig(database *db.DB) (Config, error) {
	var cfg Config
	err := database.GetSetting(SettingName, &cfg)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return cfg, nil
		}
		return cfg, err
	}
	return cfg, nil
}

func SaveConfig(database *db.DB, cfg Config) error {
	return database.SetSetting(SettingName, cfg)
}
