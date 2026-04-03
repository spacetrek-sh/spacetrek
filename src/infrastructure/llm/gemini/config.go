package gemini

import (
	"fmt"
	"time"
)

// responseTemperature is the hardcoded temperature for final response synthesis.
// Tool calling always uses 0 (deterministic). This value is used when synthesizing
// the assistant's answer — slightly non-zero for natural, conversational responses.
const responseTemperature float32 = 0.2

// Config holds Gemini adapter configuration.
type Config struct {
	APIKey          string
	Model           string
	MaxOutputTokens int32
	SystemPrompt    string
	Timeout         time.Duration
}

// DefaultConfig returns sensible defaults for the Gemini adapter.
func DefaultConfig() Config {
	return Config{
		Model:           "gemini-2.0-flash",
		MaxOutputTokens: 4096,
		Timeout:         60 * time.Second,
	}
}

// Validate checks that the configuration is usable.
func (c Config) Validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("gemini: api_key is required")
	}
	if c.Model == "" {
		return fmt.Errorf("gemini: model is required")
	}
	if c.MaxOutputTokens <= 0 {
		return fmt.Errorf("gemini: max_output_tokens must be positive, got %d", c.MaxOutputTokens)
	}
	if c.Timeout <= 0 || c.Timeout > 5*time.Minute {
		return fmt.Errorf("gemini: timeout must be between 0 and 5m, got %s", c.Timeout)
	}
	return nil
}
