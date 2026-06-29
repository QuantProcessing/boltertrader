package hyperliquid

import (
	"encoding/json"
	"errors"
)

type MixedValue json.RawMessage

func (mv *MixedValue) UnmarshalJSON(data []byte) error {
	*mv = data
	return nil
}

func (mv MixedValue) MarshalJSON() ([]byte, error) {
	return mv, nil
}

func (mv *MixedValue) String() (string, bool) {
	var s string
	if err := json.Unmarshal(*mv, &s); err != nil {
		return "", false
	}
	return s, true
}

func (mv *MixedValue) Object() (map[string]any, bool) {
	var obj map[string]any
	if err := json.Unmarshal(*mv, &obj); err != nil {
		return nil, false
	}
	return obj, true
}

func (mv *MixedValue) Array() ([]json.RawMessage, bool) {
	var arr []json.RawMessage
	if err := json.Unmarshal(*mv, &arr); err != nil {
		return nil, false
	}
	return arr, true
}

func (mv *MixedValue) Parse(v any) error {
	return json.Unmarshal(*mv, v)
}

func (mv *MixedValue) Type() string {
	if mv == nil || len(*mv) == 0 {
		return "null"
	}

	first := (*mv)[0]

	switch first {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

type MixedArray []MixedValue

func (ma *MixedArray) UnmarshalJSON(data []byte) error {
	var rawArr []MixedValue
	if err := json.Unmarshal(data, &rawArr); err != nil {
		return err
	}

	*ma = rawArr
	return nil
}

func (ma MixedArray) FirstError() error {
	for _, mv := range ma {
		if s, ok := mv.String(); ok {
			if s == "success" {
				continue
			}
			// any other string? treat as error text
			return errors.New(s)
		}
		if obj, ok := mv.Object(); ok {
			if v, ok := obj["error"]; ok {
				if msg, ok := v.(string); ok && msg != "" {
					return errors.New(msg)
				}
				// stringify unknown error shapes
				b, _ := json.Marshal(v)
				return errors.New(string(b))
			}
		}
		// Unknown shape -> generic failure
		return errors.New("cancel failed")
	}
	return nil
}
