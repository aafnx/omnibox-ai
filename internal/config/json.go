// Package config reads and validates the installation's settings files with the same
// rules and messages as the PowerShell version, so both versions accept the same files.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// object is a decoded JSON object that remembers its key order.
type object struct {
	keys   []string
	values map[string]any
}

func (o *object) has(key string) bool { _, ok := o.values[key]; return ok }

func (o *object) str(key string) (string, bool) {
	v, ok := o.values[key].(string)
	return v, ok
}

func (o *object) boolean(key string) (bool, bool) {
	v, ok := o.values[key].(bool)
	return v, ok
}

// integer accepts only JSON integers, as PowerShell's [long]/[int] checks do.
func (o *object) integer(key string) (int64, bool) {
	n, ok := o.values[key].(json.Number)
	if !ok || strings.ContainsAny(string(n), ".eE") {
		return 0, false
	}
	v, err := n.Int64()
	return v, err == nil
}

func (o *object) child(key string) (*object, bool) {
	v, ok := o.values[key].(*object)
	return v, ok
}

func (o *object) strings(key string) ([]string, bool) {
	list, ok := o.values[key].([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// parseObject decodes a JSON document whose top level must be an object.
func parseObject(data []byte) (*object, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("лишние данные после JSON")
	}
	obj, ok := value.(*object)
	if !ok {
		return nil, errors.New("нужен JSON-объект")
	}
	return obj, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &object{values: map[string]any{}}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key := keyTok.(string)
				value, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				if _, dup := obj.values[key]; !dup {
					obj.keys = append(obj.keys, key)
				}
				obj.values[key] = value
			}
			_, err := dec.Token()
			return obj, err
		case '[':
			list := []any{}
			for dec.More() {
				value, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, value)
			}
			_, err := dec.Token()
			return list, err
		}
	}
	return tok, nil
}
