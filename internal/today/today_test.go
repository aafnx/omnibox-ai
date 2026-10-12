package today

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLine(t *testing.T) {
	msk := time.FixedZone("MSK", 3*3600)
	if got := Line(time.Date(2026, 10, 12, 14, 5, 0, 0, msk)); got != "Сейчас понедельник, 12 октября 2026 г., 14:05 (UTC+03:00); дата в формате ISO — 2026-10-12." {
		t.Fatal(got)
	}
	ny := time.FixedZone("EST", -5*3600-30*60)
	if got := Line(time.Date(2027, 1, 3, 9, 0, 0, 0, ny)); !strings.Contains(got, "воскресенье, 3 января 2027 г., 09:00 (UTC-05:30)") {
		t.Fatal(got)
	}
}

func TestInject(t *testing.T) {
	now := time.Date(2026, 10, 12, 14, 5, 0, 0, time.UTC)
	sys := map[string]any{"role": "system", "content": "Ты помощник."}
	user := map[string]any{"role": "user", "content": "Какое сегодня число?"}
	got := Inject([]any{sys, user}, now)
	first := got[0].(map[string]any)["content"].(string)
	if len(got) != 2 || !strings.HasPrefix(first, "Сейчас понедельник") || !strings.HasSuffix(first, "\n\nТы помощник.") || sys["content"] != "Ты помощник." {
		t.Fatalf("into the system message, the client's map untouched: %v", got)
	}
	got = Inject([]any{user}, now)
	if len(got) != 2 || got[0].(map[string]any)["role"] != "system" || !reflect.DeepEqual(got[1], user) {
		t.Fatalf("a new system message in front: %v", got)
	}
	parts := map[string]any{"role": "system", "content": []any{map[string]any{"type": "text", "text": "x"}}}
	if got = Inject([]any{parts, user}, now); len(got) != 3 {
		t.Fatalf("a system message with parts stays as is: %v", got)
	}
}
