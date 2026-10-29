package queue

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"unicode/utf8"
)

// ExecutionPayload refuses legacy execution and uses the SDK's stored
// immutable canonical bytes for v1. Go verifies identity; it never manufactures
// a competing JSON serialization or task hash.
func (t *TaskRun) ExecutionPayload(maxBytes int) (json.RawMessage, error) {
	if t.SpecVersion == 0 {
		return nil, ErrLegacySpec
	}
	if t.SpecVersion != 1 || t.CanonicalSpecText == nil {
		return nil, errors.New("unsupported or incomplete task spec version")
	}
	raw := []byte(*t.CanonicalSpecText)
	if len(raw) > maxBytes || !utf8.Valid(raw) {
		return nil, errors.New("canonical task payload exceeds byte limit or is not UTF-8")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != t.SpecHash {
		return nil, errors.New("canonical task hash does not match stored bytes")
	}
	canonical, err := readSpecJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid canonical task JSON: %w", err)
	}
	projection, err := readSpecJSON(t.SpecJSON)
	if err != nil || !equalJSON(canonical, projection) {
		return nil, errors.New("task spec_json disagrees with immutable canonical bytes")
	}
	spec, ok := canonical.(map[string]any)
	if !ok {
		return nil, errors.New("task spec must be an object")
	}
	integerEqual := func(value any, want int) bool {
		number, ok := value.(json.Number)
		return ok && equalNumber(number, json.Number(fmt.Sprint(want)))
	}
	optionalEqual := func(value any, want *string) bool {
		if want == nil {
			return value == nil
		}
		actual, ok := value.(string)
		return ok && actual == *want
	}
	execution, ok := spec["exec"].(map[string]any)
	if !ok || !integerEqual(spec["v"], 1) || spec["backend_alias"] != t.BackendAlias || spec["queue_name"] != t.QueueName ||
		!integerEqual(spec["priority"], t.Priority) || !optionalEqual(spec["task_path"], t.TaskPath) ||
		!optionalEqual(spec["lock_key"], t.LockKey) || !optionalEqual(spec["concurrency_key"], t.ConcurrencyKey) ||
		!integerEqual(spec["concurrency_limit"], t.ConcurrencyLimit) ||
		!integerEqual(execution["timeout_seconds"], t.TimeoutSeconds) || !integerEqual(execution["max_attempts"], t.MaxAttempts) {
		return nil, errors.New("task queue/execution projections disagree with canonical spec")
	}
	return raw, nil
}

// UseNumber and decimal equality avoid losing large researcher-supplied integer
// arguments, and allow PostgreSQL jsonb's exponent/decimal formatting changes.
// Duplicate object keys and deeply nested values are rejected, not collapsed.
func readSpecJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("JSON is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 64 {
			return nil, errors.New("task JSON exceeds nesting limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return token, nil
		}
		switch delimiter {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, errors.New("non-string JSON key")
				}
				if _, exists := object[name]; exists {
					return nil, errors.New("duplicate JSON object key")
				}
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				object[name] = value
			}
			if close, err := decoder.Token(); err != nil || close != json.Delim('}') {
				return nil, errors.New("incomplete JSON object")
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			if close, err := decoder.Token(); err != nil || close != json.Delim(']') {
				return nil, errors.New("incomplete JSON array")
			}
			return array, nil
		default:
			return nil, errors.New("unexpected JSON delimiter")
		}
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing task JSON")
	}
	return value, nil
}

func equalJSON(left, right any) bool {
	switch l := left.(type) {
	case map[string]any:
		r, ok := right.(map[string]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for key, value := range l {
			other, exists := r[key]
			if !exists || !equalJSON(value, other) {
				return false
			}
		}
		return true
	case []any:
		r, ok := right.([]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for i := range l {
			if !equalJSON(l[i], r[i]) {
				return false
			}
		}
		return true
	case json.Number:
		r, ok := right.(json.Number)
		return ok && equalNumber(l, r)
	case string:
		r, ok := right.(string)
		return ok && l == r
	case bool:
		r, ok := right.(bool)
		return ok && l == r
	case nil:
		return right == nil
	default:
		return false
	}
}

// A normalized digit string plus a decimal exponent is sufficient for exact
// equality. Never allocate a power of ten from an untrusted exponent.
func equalNumber(left, right json.Number) bool {
	normalize := func(number json.Number) (string, *big.Int) {
		text := string(number)
		exponent := new(big.Int)
		if at := strings.IndexAny(text, "eE"); at >= 0 {
			exponent.SetString(text[at+1:], 10)
			text = text[:at]
		}
		negative := strings.HasPrefix(text, "-")
		text = strings.TrimPrefix(text, "-")
		if at := strings.IndexByte(text, '.'); at >= 0 {
			exponent.Sub(exponent, big.NewInt(int64(len(text)-at-1)))
			text = text[:at] + text[at+1:]
		}
		text = strings.TrimLeft(text, "0")
		if text == "" {
			return "0", new(big.Int)
		}
		trimmed := strings.TrimRight(text, "0")
		exponent.Add(exponent, big.NewInt(int64(len(text)-len(trimmed))))
		if negative {
			trimmed = "-" + trimmed
		}
		return trimmed, exponent
	}
	l, le := normalize(left)
	r, re := normalize(right)
	return l == r && le.Cmp(re) == 0
}
