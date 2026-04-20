package httputil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	"github.com/spacetrek-sh/spacetrek/pkg/validation"
)

const defaultMaxMemory int64 = 32 << 20 // 32 MB

// DecodeJSON reads the request body and decodes it as JSON into dst.
// Unknown fields in the JSON are rejected. Returns a structured AppError on
// any parse failure so callers can pass it directly to WriteError.
func DecodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return exception.BadRequest("request body is empty")
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return jsonDecodeError(err)
	}
	return nil
}

// BindJSON decodes JSON into dst and then validates it using struct tags.
// Combines DecodeJSON + validation.Struct in one call.
func BindJSON(r *http.Request, dst any) error {
	if err := DecodeJSON(r, dst); err != nil {
		return err
	}
	return validation.Struct(dst)
}

// ParseMultipart parses the incoming multipart/form-data body.
// maxMemory controls how much of the file parts are kept in RAM before
// spilling to disk; pass ≤ 0 to use the 32 MB default.
// The returned *multipart.Form is also accessible via r.MultipartForm after
// this call.
func ParseMultipart(r *http.Request, maxMemory int64) (*multipart.Form, error) {
	if maxMemory <= 0 {
		maxMemory = defaultMaxMemory
	}
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		if errors.Is(err, multipart.ErrMessageTooLarge) {
			return nil, exception.BadRequest(
				fmt.Sprintf("multipart body exceeds the %d MB size limit", maxMemory>>20),
			)
		}
		return nil, exception.BadRequest("failed to parse multipart form: " + err.Error())
	}
	return r.MultipartForm, nil
}

// FormValue returns a single string value from a parsed multipart or
// URL-encoded form. Returns a BadRequest error when the field is missing.
func FormValue(r *http.Request, field string) (string, error) {
	v := r.FormValue(field)
	if v == "" {
		return "", exception.BadRequest(fmt.Sprintf("form field %q is required", field))
	}
	return v, nil
}

// FormFile retrieves the first file uploaded under the given field name from a
// parsed multipart form. Returns a BadRequest error when no file is found.
func FormFile(r *http.Request, field string) (multipart.File, *multipart.FileHeader, error) {
	f, fh, err := r.FormFile(field)
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return nil, nil, exception.BadRequest(
				fmt.Sprintf("file field %q is required", field),
			)
		}
		return nil, nil, exception.BadRequest("failed to read uploaded file: " + err.Error())
	}
	return f, fh, nil
}

// jsonDecodeError converts json decode errors into structured AppErrors.
func jsonDecodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError

	switch {
	case errors.As(err, &syntaxErr):
		return exception.BadRequest(
			fmt.Sprintf("malformed JSON (syntax error at byte offset %d)", syntaxErr.Offset),
		)
	case errors.As(err, &typeErr):
		return exception.BadRequest(
			fmt.Sprintf("invalid type for field %q: expected %s", typeErr.Field, typeErr.Type),
		)
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return exception.BadRequest("request body is empty or incomplete")
	default:
		return exception.BadRequest("could not parse request body: " + err.Error())
	}
}
