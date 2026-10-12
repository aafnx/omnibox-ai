package config

import "testing"

// Suggestions are written by the chat's model, an OpenRouter or a ChatGPT one: never another
// local model, which would have to be started for them.
func TestFollowUpModel(t *testing.T) {
	for model, ok := range map[string]bool{"": true, "cloud:z-ai/glm-5.3-flash": true, "prismml:a.gguf": false, "ollama:qwen3:8b": false, "cloud:bad": false, "chatgpt:gpt-5.5": true, "chatgpt:../x": false} {
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

// ChatGPT models are named chatgpt:<id>; like OpenRouter's they have no launch settings and
// never start after boot.
func TestChatGPTKeys(t *testing.T) {
	for key, ok := range map[string]bool{"chatgpt:gpt-5.5": true, "chatgpt:gpt-6.1-sol": true, "chatgpt:": false, "chatgpt:a/b": false, "chatgpt:a b": false, "chatgpt:..": false} {
		kind, _, valid := SplitKey(key)
		if valid != ok || (ok && kind != "chatgpt") {
			t.Errorf("%q: %q %v", key, kind, valid)
		}
	}
	c := validConfig()
	c.Models = map[string]*ModelPrefs{"chatgpt:gpt-5.5": {Settings: map[string]string{"context": "8192"}}}
	if c.Validate() == nil {
		t.Error("a ChatGPT model has no launch settings")
	}
	c = validConfig()
	c.Panel.StartModel = "chatgpt:gpt-5.5"
	if c.Validate() == nil {
		t.Error("a ChatGPT model is not started after boot")
	}
	c = validConfig()
	c.Favorites = []string{"chatgpt:gpt-5.5"}
	c.ChatGPT = &ChatGPT{ExposeAPI: true}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !c.ChatGPTSettings().ExposeAPI || Default().ChatGPTSettings().ExposeAPI {
		t.Error("ChatGPT is shown to /v1 only when the owner turns it on")
	}
}
