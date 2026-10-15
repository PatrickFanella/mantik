package workerplan

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

func unique(d *json.Decoder, depth int) error {
	if depth > 32 {
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
				key, err := d.Token()
				if err != nil {
					return ErrInvalid
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return ErrInvalid
				}
				seen[name] = true
				if unique(d, depth+1) != nil {
					return ErrInvalid
				}
			}
		case '[':
			for d.More() {
				if unique(d, depth+1) != nil {
					return ErrInvalid
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
func names(supplied, expected any) bool {
	switch s := supplied.(type) {
	case map[string]any:
		e, ok := expected.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range s {
			target, ok := e[k]
			if !ok || !names(v, target) {
				return false
			}
		}
	case []any:
		e, ok := expected.([]any)
		if !ok || len(s) != len(e) {
			return false
		}
		for i, v := range s {
			if !names(v, e[i]) {
				return false
			}
		}
	}
	return true
}
func readJSON(raw []byte, value any) error {
	if !utf8.Valid(raw) || unique(json.NewDecoder(bytes.NewReader(raw)), 0) != nil {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return ErrInvalid
	}
	var supplied, expected any
	if json.Unmarshal(raw, &supplied) != nil || json.Unmarshal(canonical, &expected) != nil || !names(supplied, expected) {
		return ErrInvalid
	}
	return nil
}
func ReadJob(input io.Reader) (Job, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxJobBytes+1))
	if err != nil || len(raw) > MaxJobBytes {
		return Job{}, ErrInvalid
	}
	var j Job
	if readJSON(raw, &j) != nil {
		return Job{}, ErrInvalid
	}
	return j, j.Validate()
}
func ReadResult(input io.Reader, j Job) (Result, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxResultBytes+1))
	if err != nil || len(raw) > MaxResultBytes {
		return Result{}, ErrInvalid
	}
	var r Result
	if readJSON(raw, &r) != nil {
		return Result{}, ErrInvalid
	}
	return r, r.Validate(j)
}

// ReadDatasetShape rejects ambiguous JSON field names before native admission.
// The caller supplies a typed dataset; this helper grants no data qualification.
func ReadDatasetShape(raw []byte, value any) error {
	if len(raw) > MaxInputBytes {
		return ErrInvalid
	}
	return readJSON(raw, value)
}
