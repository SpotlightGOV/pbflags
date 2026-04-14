// Package flagfmt provides shared types and formatting for flag values
// and condition-chain JSON. All packages that read or write the conditions
// JSONB column should use these types to avoid drift.
package flagfmt

import "encoding/json"

// StoredCondition is the JSON schema for a single entry in the conditions
// JSONB column. Sync writes it, evaluator and admin read it.
type StoredCondition struct {
	CEL     *string         `json:"cel"`
	Value   json.RawMessage `json:"value"`
	Comment string          `json:"comment,omitempty"`
}
