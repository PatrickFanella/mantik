package trial

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"unicode/utf8"
)

const MaxBytes = (1 << 20) + 4096

var field = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func Read(reader io.Reader) (Command, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes || !utf8.Valid(raw) {
		return Command{}, ErrInvalid
	}
	if err = unique(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return Command{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var c Command
	if decoder.Decode(&c) != nil || decoder.Decode(new(any)) != io.EOF {
		return c, ErrInvalid
	}
	// Existing v1 library entries use Go field names (ID, ContractVersion,
	// etc.). Enforce exact names against the decoded wire shape without changing
	// that pinned representation or accepting case-insensitive aliases.
	canonical, _ := json.Marshal(c)
	var supplied, expected any
	if json.Unmarshal(raw, &supplied) != nil || json.Unmarshal(canonical, &expected) != nil || !canonicalNames(supplied, expected) {
		return c, ErrInvalid
	}
	recipe, _ := json.Marshal(c.Recipe)
	if len(recipe) > 1<<20 {
		return c, ErrInvalid
	}
	return c, c.Validate()
}
func unique(d *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return ErrInvalid
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return ErrInvalid
				}
				name, ok := k.(string)
				if !ok || !field.MatchString(name) || seen[name] {
					return ErrInvalid
				}
				seen[name] = true
				if err := unique(d, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := unique(d, depth+1); err != nil {
					return err
				}
			}
		default:
			return ErrInvalid
		}
		if _, err = d.Token(); err != nil {
			return ErrInvalid
		}
	}
	return nil
}

func canonicalNames(supplied, expected any) bool {
	switch s := supplied.(type) {
	case map[string]any:
		e, ok := expected.(map[string]any)
		if !ok {
			return false
		}
		for name, value := range s {
			target, ok := e[name]
			if !ok || !canonicalNames(value, target) {
				return false
			}
		}
	case []any:
		e, ok := expected.([]any)
		if !ok || len(s) != len(e) {
			return false
		}
		for i, value := range s {
			if !canonicalNames(value, e[i]) {
				return false
			}
		}
	}
	return true
}

func ReadAttemptCommand(reader io.Reader) (AttemptCommand, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil || len(raw) > 4096 || !utf8.Valid(raw) {
		return AttemptCommand{}, ErrInvalid
	}
	if err = unique(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return AttemptCommand{}, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var c AttemptCommand
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, ErrInvalid
	}
	canonical, _ := json.Marshal(c)
	var supplied, expected any
	if json.Unmarshal(raw, &supplied) != nil || json.Unmarshal(canonical, &expected) != nil || !canonicalNames(supplied, expected) {
		return c, ErrInvalid
	}
	return c, c.Validate()
}

func ReadAttemptCancellation(reader io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil || len(raw) > 4096 || !utf8.Valid(raw) {
		return "", ErrInvalid
	}
	if err = unique(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return "", err
	}
	var c struct {
		ReceiverID string `json:"receiver_id"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return "", ErrInvalid
	}
	canonical, _ := json.Marshal(c)
	var supplied, expected any
	if json.Unmarshal(raw, &supplied) != nil || json.Unmarshal(canonical, &expected) != nil || !canonicalNames(supplied, expected) {
		return "", ErrInvalid
	}
	return c.ReceiverID, nil
}
