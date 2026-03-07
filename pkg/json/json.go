// Package jsonutil provides thin helpers around encoding/json that return
// structured AppErrors instead of raw error values.
package jsonutil

import (
	"bytes"
	"encoding/json"

	"github.com/kumori-sh/spacetrk/pkg/exception"
)

// Marshal encodes v to JSON. Returns an Internal AppError on failure.
func Marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, exception.Internal(err)
	}
	return b, nil
}

// MarshalIndent encodes v to indented JSON. Returns an Internal AppError on failure.
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	b, err := json.MarshalIndent(v, prefix, indent)
	if err != nil {
		return nil, exception.Internal(err)
	}
	return b, nil
}

// Unmarshal decodes JSON data into v. Returns a BadRequest AppError on failure
// (assuming the data comes from external input).
func Unmarshal(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return exception.BadRequest("invalid JSON: " + err.Error())
	}
	return nil
}

// NewDecoder creates a *json.Decoder that disallows unknown fields — prefer
// this over bare json.NewDecoder when parsing user-supplied input.
func NewDecoder(data []byte) *json.Decoder {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec
}
