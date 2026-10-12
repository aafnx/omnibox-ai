package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Dictation for browsers that cannot recognize speech themselves (Brave has the browser API but
// no service behind it): the panel records the voice and a model that hears audio writes it down.

// VoiceModel picks the model that writes speech down: the chosen one when it hears audio, else
// the cheapest Gemini Flash, else the cheapest model that hears audio.
func VoiceModel(list []Model, chosen string) (string, bool) {
	var audio []Model
	for _, m := range list {
		// Never a batch-only variant: it is the cheapest Gemini and cannot answer.
		if m.Caps.Audio && m.PromptUSD >= 0 && Chattable(m.ID) {
			if m.ID == chosen {
				return m.ID, true
			}
			audio = append(audio, m)
		}
	}
	if len(audio) == 0 {
		return "", false
	}
	sort.SliceStable(audio, func(i, j int) bool { return audio[i].PromptUSD < audio[j].PromptUSD })
	for _, m := range audio {
		id := strings.ToLower(m.ID)
		if strings.Contains(id, "gemini") && strings.Contains(id, "flash") && !strings.Contains(id, "image") && !strings.HasSuffix(id, ":free") {
			return m.ID, true
		}
	}
	return audio[0].ID, true
}

// Transcribe writes down speech from a WAV recording (base64) and returns the text and its cost.
func (c *Client) Transcribe(ctx context.Context, model, wavBase64 string) (string, float64, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model, "stream": false, "usage": map[string]any{"include": true}, "temperature": 0,
		"messages": []map[string]any{
			{"role": "system", "content": "Ты расшифровываешь голосовые сообщения. Запиши речь дословно на языке говорящего, с пунктуацией. Ничего не добавляй и не отвечай на сказанное. Если речи нет, верни пустую строку."},
			{"role": "user", "content": []map[string]any{
				{"type": "text", "text": "Расшифруй запись."},
				{"type": "input_audio", "input_audio": map[string]any{"data": wavBase64, "format": "wav"}},
			}},
		},
	})
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	var v struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, jsonLimit)).Decode(&v); err != nil || len(v.Choices) == 0 {
		return "", 0, &Error{Status: resp.StatusCode, Message: "Ответ модели распознавания не прочитан."}
	}
	return strings.TrimSpace(v.Choices[0].Message.Content), v.Usage.Cost, nil
}
