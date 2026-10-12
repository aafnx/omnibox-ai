package config

import "testing"

// Suggestions are written by the chat's model or an OpenRouter one: never another local model,
// which would have to be started for them.
func TestFollowUpModel(t *testing.T) {
	for model, ok := range map[string]bool{"": true, "cloud:z-ai/glm-5.3-flash": true, "prismml:a.gguf": false, "ollama:qwen3:8b": false, "cloud:bad": false} {
		c := DefaultChat()
		c.FollowUpModel = model
		if err := c.Validate(); (err == nil) != ok {
			t.Errorf("%q: %v", model, err)
		}
	}
	if DefaultChat().FollowUps {
		t.Error("suggestions are off by default")
	}
}
