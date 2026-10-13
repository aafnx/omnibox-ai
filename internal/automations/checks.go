package automations

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Checks are the ways a field's value is verified after the model read it: a wrong digit in a
// passport is caught here, not by the model's word.
var Checks = []string{"", "digits", "number", "date", "date_past", "phone", "email", "passport_series", "passport_number", "division_code", "inn", "snils", "ogrn", "kpp", "bik", "account"}

// CheckNames say what each check is, for the model that proposes a field's type.
var CheckNames = map[string]string{
	"": "текст", "digits": "только цифры", "number": "число или сумма", "date": "дата", "date_past": "дата не в будущем (рождения, выдачи)",
	"phone": "телефон", "email": "email", "passport_series": "серия паспорта РФ", "passport_number": "номер паспорта РФ",
	"division_code": "код подразделения", "inn": "ИНН", "snils": "СНИЛС", "ogrn": "ОГРН или ОГРНИП", "kpp": "КПП", "bik": "БИК", "account": "расчётный или корреспондентский счёт",
}

// CheckValue explains what is wrong with a value, "" when it passes or is empty.
func CheckValue(check, value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	digits := onlyDigits(v)
	switch check {
	case "digits":
		if digits != strings.ReplaceAll(v, " ", "") {
			return "Должны быть только цифры."
		}
	case "number":
		if !number.MatchString(strings.NewReplacer(" ", "", "\u00a0", "").Replace(v)) {
			return "Должно быть число."
		}
	case "phone":
		if n := len(digits); n < 10 || n > 15 || !phone.MatchString(v) {
			return "Не похоже на телефон: 10–15 цифр."
		}
	case "email":
		if !email.MatchString(v) {
			return "Не похоже на email."
		}
	case "ogrn":
		if !ogrnValid(digits) || len(digits) != countDigitsAndSpaces(v) {
			return "ОГРН не сходится: 13 цифр (ОГРНИП — 15) с контрольной цифрой."
		}
	case "kpp":
		if !kpp.MatchString(strings.ReplaceAll(v, " ", "")) {
			return "КПП — 9 знаков: 4 цифры, 2 цифры или буквы, 3 цифры."
		}
	case "bik":
		if len(digits) != 9 || len(digits) != countDigitsAndSpaces(v) || !strings.HasPrefix(digits, "0") {
			return "БИК — 9 цифр, начинается с 0."
		}
	case "account":
		if len(digits) != 20 || len(digits) != countDigitsAndSpaces(v) {
			return "Счёт — 20 цифр."
		}
	case "date", "date_past":
		d, ok := parseDate(v)
		if !ok {
			return "Не похоже на дату (ДД.ММ.ГГГГ)."
		}
		if check == "date_past" && d.After(time.Now()) {
			return "Дата в будущем."
		}
		if d.Year() < 1900 {
			return "Слишком ранняя дата."
		}
	case "passport_series":
		if len(digits) != 4 || len(digits) != countDigitsAndSpaces(v) {
			return "Серия паспорта — 4 цифры."
		}
	case "passport_number":
		if len(digits) != 6 || len(digits) != countDigitsAndSpaces(v) {
			return "Номер паспорта — 6 цифр."
		}
	case "division_code":
		if !divisionCode.MatchString(v) {
			return "Код подразделения — в виде 123-456."
		}
	case "inn":
		if !innValid(digits) || len(digits) != countDigitsAndSpaces(v) {
			return "ИНН не сходится: 10 или 12 цифр с контрольной суммой."
		}
	case "snils":
		if !snilsValid(digits) {
			return "СНИЛС не сходится: 11 цифр с контрольной суммой."
		}
	}
	return ""
}

var (
	divisionCode = regexp.MustCompile(`^\d{3}-\d{3}$`)
	number       = regexp.MustCompile(`^-?\d+([.,]\d+)?$`)
	phone        = regexp.MustCompile(`^\+?[\d\s()\-]+$`)
	email        = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]{2,}$`)
	kpp          = regexp.MustCompile(`^\d{4}[\dA-ZА-Я]{2}\d{3}$`)
)

// ogrnValid checks the control digit: the number without it modulo 11 (13 digits) or 13 (15).
func ogrnValid(d string) bool {
	var mod uint64
	switch len(d) {
	case 13:
		mod = 11
	case 15:
		mod = 13
	default:
		return false
	}
	n, err := strconv.ParseUint(d[:len(d)-1], 10, 64)
	if err != nil {
		return false
	}
	return int(n%mod%10) == int(d[len(d)-1]-'0')
}

func onlyDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}

// countDigitsAndSpaces is the length without spaces: "92 04" is four digits, "92-0A" is not.
func countDigitsAndSpaces(s string) int { return len(strings.ReplaceAll(s, " ", "")) }

var months = map[string]time.Month{"января": 1, "февраля": 2, "марта": 3, "апреля": 4, "мая": 5, "июня": 6, "июля": 7, "августа": 8, "сентября": 9, "октября": 10, "ноября": 11, "декабря": 12}

// parseDate reads 12.10.2026, 2026-10-12 and «12» октября 2026 г.
func parseDate(s string) (time.Time, bool) {
	for _, layout := range []string{"02.01.2006", "2.1.2006", "2006-01-02"} {
		if d, err := time.Parse(layout, s); err == nil {
			return d, true
		}
	}
	f := strings.Fields(strings.NewReplacer("«", " ", "»", " ", "г.", " ", "года", " ").Replace(strings.ToLower(s)))
	if len(f) == 3 {
		day, e1 := strconv.Atoi(f[0])
		year, e2 := strconv.Atoi(f[2])
		if m, ok := months[f[1]]; ok && e1 == nil && e2 == nil {
			d := time.Date(year, m, day, 0, 0, 0, 0, time.UTC)
			if d.Day() == day {
				return d, true
			}
		}
	}
	return time.Time{}, false
}

func innValid(d string) bool {
	sum := func(weights []int) int {
		s := 0
		for i, w := range weights {
			s += w * int(d[i]-'0')
		}
		return s % 11 % 10
	}
	switch len(d) {
	case 10:
		return sum([]int{2, 4, 10, 3, 5, 9, 4, 6, 8}) == int(d[9]-'0')
	case 12:
		return sum([]int{7, 2, 4, 10, 3, 5, 9, 4, 6, 8}) == int(d[10]-'0') && sum([]int{3, 7, 2, 4, 10, 3, 5, 9, 4, 6, 8}) == int(d[11]-'0')
	}
	return false
}

func snilsValid(d string) bool {
	if len(d) != 11 {
		return false
	}
	s := 0
	for i := 0; i < 9; i++ {
		s += int(d[i]-'0') * (9 - i)
	}
	want, _ := strconv.Atoi(d[9:])
	switch {
	case s < 100:
	case s == 100 || s == 101:
		s = 0
	default:
		s %= 101
		if s == 100 {
			s = 0
		}
	}
	return s == want
}

var varPattern = regexp.MustCompile(`\{\{\s*([^{}]{1,80}?)\s*\}\}`)

// Vars are the {{names}} of a prompt, each once, in order.
func Vars(prompt string) []string {
	var out []string
	for _, m := range varPattern.FindAllStringSubmatch(prompt, -1) {
		if !contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// FillVars puts the values into the prompt; a missing one is refused, so a task never runs with
// a hole in it.
func FillVars(prompt string, values map[string]string) (string, error) {
	var missing []string
	out := varPattern.ReplaceAllStringFunc(prompt, func(m string) string {
		name := varPattern.FindStringSubmatch(m)[1]
		v := strings.TrimSpace(values[name])
		if v == "" {
			if !contains(missing, name) {
				missing = append(missing, name)
			}
			return m
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("Заполните: %s.", strings.Join(missing, ", "))
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Schedule runs a task by itself: every day or on chosen weekdays at a time, or every few hours
// from that time. The PC's clock and time zone are used; a run missed while the PC was off is not
// made up later.
type Schedule struct {
	Enabled bool   `json:"enabled"`
	Every   string `json:"every"` // "day", "week" or "hours"
	At      string `json:"at"`    // "08:30"
	Days    []int  `json:"days,omitempty"`
	Hours   int    `json:"hours,omitempty"`
}

func (s *Schedule) check() error {
	if !s.Enabled {
		return nil
	}
	if _, _, ok := clock(s.At); !ok {
		return errors.New("Время запуска — в виде 08:30.")
	}
	switch s.Every {
	case "day":
	case "week":
		if len(s.Days) == 0 {
			return errors.New("Выберите дни недели для запуска.")
		}
		for _, d := range s.Days {
			if d < 1 || d > 7 {
				return errors.New("Дни недели — от понедельника до воскресенья.")
			}
		}
	case "hours":
		if s.Hours < 1 || s.Hours > 24 {
			return errors.New("Интервал — от 1 до 24 часов.")
		}
	default:
		return errors.New("Неизвестное расписание.")
	}
	return nil
}

func clock(at string) (int, int, bool) {
	h, m, ok := strings.Cut(at, ":")
	hh, e1 := strconv.Atoi(h)
	mm, e2 := strconv.Atoi(m)
	return hh, mm, ok && e1 == nil && e2 == nil && hh >= 0 && hh < 24 && mm >= 0 && mm < 60 && len(m) == 2
}

// Next is the first run after the moment, in its time zone.
func (s *Schedule) Next(after time.Time) (time.Time, bool) {
	if s == nil || !s.Enabled || s.check() != nil {
		return time.Time{}, false
	}
	hh, mm, _ := clock(s.At)
	day := time.Date(after.Year(), after.Month(), after.Day(), hh, mm, 0, 0, after.Location())
	switch s.Every {
	case "hours":
		// From today's time every N hours, through midnight and on.
		t := day.AddDate(0, 0, -1)
		for !t.After(after) {
			t = t.Add(time.Duration(s.Hours) * time.Hour)
		}
		return t, true
	default:
		for i := 0; i < 8; i++ {
			t := day.AddDate(0, 0, i)
			if !t.After(after) {
				continue
			}
			wd := int(t.Weekday())
			if wd == 0 {
				wd = 7
			}
			if s.Every == "day" || containsInt(s.Days, wd) {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

func containsInt(list []int, n int) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}
