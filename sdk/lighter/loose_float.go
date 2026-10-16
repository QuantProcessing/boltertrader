package lighter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// LooseFloat decodes a statistic that Lighter publishes as a JSON number for
// perpetual markets but as a decimal string (or null) for spot markets in the
// same orderBookDetails payload. These fields are informational only; a
// representation change must never make the instrument catalog unreadable.
type LooseFloat float64

func (value *LooseFloat) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*value = 0
		return nil
	}
	if data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		if text == "" {
			*value = 0
			return nil
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return fmt.Errorf("lighter: loose float %q: %w", text, err)
		}
		*value = LooseFloat(parsed)
		return nil
	}
	var parsed float64
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*value = LooseFloat(parsed)
	return nil
}
