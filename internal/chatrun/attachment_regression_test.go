package chatrun

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/attach"
)

func TestAggregateAttachmentsRefusedBeforeStoringOrGenerating(t *testing.T) {
	g := newRig(t)
	local.Caps.Vision = true
	defer func() { local.Caps.Vision = false }()
	encoded := base64.StdEncoding.EncodeToString(make([]byte, attach.MaxFile))
	_, err := g.r.prepare(context.Background(), Request{ChatID: g.chat.ID, Model: local.Key, Text: "Фото", Files: []File{
		{Name: "1.png", Data: encoded}, {Name: "2.png", Data: encoded}, {Name: "3.png", Data: encoded},
		{Name: "4.png", Data: base64.StdEncoding.EncodeToString([]byte("x"))},
	}})
	if err == nil || !strings.Contains(err.Error(), "75 МБ") {
		t.Fatalf("aggregate limit did not refuse attachments: %v", err)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if len(c.Messages) != 0 || len(g.bodies) != 0 {
		t.Fatal("a refused attachment request changed the chat or reached the model")
	}
}
