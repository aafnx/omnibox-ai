package config

import (
	"errors"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Storage is storage.json: log retention, size warning and request journal mode.
type Storage struct {
	LogRetentionDays int // 0 keeps logs forever
	LogWarnMB        int
	RequestLog       string // metadata or content
}

// DefaultStorage matches the PowerShell defaults.
func DefaultStorage() Storage {
	return Storage{LogRetentionDays: 30, LogWarnMB: 200, RequestLog: "metadata"}
}

var digits = regexp.MustCompile(`^\d{1,6}$`)

// LoadStorage reads storage.json. Like the PowerShell version it never fails: a missing,
// damaged or invalid value falls back to its default.
func LoadStorage(root string) Storage {
	s := DefaultStorage()
	data, err := platform.ReadShared(filepath.Join(root, "storage.json"))
	if err != nil {
		return s
	}
	obj, err := parseObject(data)
	if err != nil {
		return s
	}
	for _, key := range []string{"log_retention_days", "log_warn_mb", "request_log"} {
		value, ok := obj.values[key]
		if !ok {
			continue
		}
		text := scalarText(value)
		if key == "request_log" {
			if v, err := StorageRequestLog(text); err == nil {
				s.RequestLog = v
			}
			continue
		}
		if v, err := StorageNumber(key, text); err == nil {
			if key == "log_retention_days" {
				s.LogRetentionDays = v
			} else {
				s.LogWarnMB = v
			}
		}
	}
	return s
}

func scalarText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case interface{ String() string }:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

// StorageRequestLog validates the request journal mode.
func StorageRequestLog(text string) (string, error) {
	if text != "metadata" && text != "content" {
		return "", errors.New("Журнал запросов: metadata или content.")
	}
	return text, nil
}

// StorageNumber validates retention days and the warning threshold.
func StorageNumber(key, text string) (int, error) {
	if !digits.MatchString(text) {
		return 0, errors.New("Срок хранения и порог объёма — целые числа.")
	}
	n, _ := strconv.Atoi(text)
	if key == "log_retention_days" && n > 365 {
		return 0, errors.New("Срок хранения журналов: от 0 (не удалять) до 365 дней.")
	}
	if key == "log_warn_mb" && (n < 10 || n > 100000) {
		return 0, errors.New("Порог объёма журналов: от 10 до 100000 МБ.")
	}
	return n, nil
}
