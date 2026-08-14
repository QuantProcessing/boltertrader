package httpapi

// Strict JSON decoding for mutation bodies. A request that misspells a field,
// repeats a key, or trails a second JSON value is rejected rather than silently
// losing the intent — a canonical body is the only body this service accepts.
//
// These helpers used to carry a credential-bundle special case, because account
// creation posted secret material here. Accounts now live behind the gateway's
// internal plane, so nothing reaching this package is allowed to carry a
// credential bundle at all; such a key is refused as an unknown field.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
)

const mutationBodyLimit = 1 << 20

func decodeStrict(r *http.Request, target any) error {
	body, err := readMutationBody(r)
	if err != nil {
		return err
	}
	if err := validateExactJSONKeys(body, target); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func readMutationBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, mutationBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > mutationBodyLimit {
		return nil, errors.New("request body exceeds limit")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return body, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := scanUniqueJSONObjectKeys(decoder); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("request body must contain one JSON value")
	}
	return body, nil
}

// validateExactJSONKeys refuses fields the target does not name. It runs before
// the decode so a rejected body never partially populates the target.
func validateExactJSONKeys(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer {
		return errors.New("request body target is invalid")
	}
	return validateJSONValueKeys(value, targetType.Elem())
}

func validateJSONValueKeys(value any, targetType reflect.Type) error {
	for targetType.Kind() == reflect.Pointer {
		targetType = targetType.Elem()
	}
	switch targetType.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		fields := make(map[string]reflect.StructField, targetType.NumField())
		for index := 0; index < targetType.NumField(); index++ {
			field := targetType.Field(index)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.PkgPath == "" && name != "" && name != "-" {
				fields[name] = field
			}
		}
		for key, child := range object {
			field, exists := fields[key]
			if !exists {
				return fmt.Errorf("request body contains non-canonical field %q", key)
			}
			if err := validateJSONValueKeys(child, field.Type); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return nil
		}
		for _, item := range items {
			if err := validateJSONValueKeys(item, targetType.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// scanUniqueJSONObjectKeys rejects duplicate keys case-insensitively. Go's
// decoder keeps the last of a repeated key, which would let a body carry one
// value past review and a different one into the handler.
func scanUniqueJSONObjectKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("request body contains invalid object key")
			}
			folded := strings.ToLower(key)
			if _, exists := seen[folded]; exists {
				return fmt.Errorf("request body contains duplicate key %q", key)
			}
			seen[folded] = struct{}{}
			if err := scanUniqueJSONObjectKeys(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("request body contains invalid object")
		}
	case '[':
		for decoder.More() {
			if err := scanUniqueJSONObjectKeys(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("request body contains invalid array")
		}
	default:
		return errors.New("request body contains invalid JSON delimiter")
	}
	return nil
}
