// Package today tells a model the current date and time. Models know only their training date:
// without this line "today", "tomorrow" and "this week" are guesses. The line is in Russian, with
// the weekday and the computer's UTC offset, so a model reads it at a glance.
package today

import (
	"fmt"
	"time"
)

var (
	weekdays = [...]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}
	months   = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
)

// Line is "Сейчас понедельник, 12 октября 2026 г., 14:35 (UTC+03:00); дата в формате ISO — 2026-10-12."
func Line(now time.Time) string {
	_, offset := now.Zone()
	sign := '+'
	if offset < 0 {
		sign, offset = '-', -offset
	}
	return fmt.Sprintf("Сейчас %s, %d %s %d г., %02d:%02d (UTC%c%02d:%02d); дата в формате ISO — %s.",
		weekdays[now.Weekday()], now.Day(), months[now.Month()-1], now.Year(), now.Hour(), now.Minute(),
		sign, offset/3600, offset%3600/60, now.Format("2006-01-02"))
}

// Prompt is Line with how to use it: relative dates count from it, and it wins over what the
// model remembers.
func Prompt(now time.Time) string {
	return Line(now) + " Считай «сегодня», «завтра», «вчера» и дни недели от этой даты; она важнее того, что ты помнишь по обучению."
}

// Inject adds Prompt to an OpenAI-style message list: to the first system message when its
// content is text, otherwise as a new system message in front. Other messages are not touched.
func Inject(messages []any, now time.Time) []any {
	line := Prompt(now)
	if len(messages) > 0 {
		if first, ok := messages[0].(map[string]any); ok && first["role"] == "system" {
			if text, ok := first["content"].(string); ok {
				copied := make(map[string]any, len(first))
				for k, v := range first {
					copied[k] = v
				}
				copied["content"] = line + "\n\n" + text
				return append([]any{copied}, messages[1:]...)
			}
		}
	}
	return append([]any{map[string]any{"role": "system", "content": line}}, messages...)
}
