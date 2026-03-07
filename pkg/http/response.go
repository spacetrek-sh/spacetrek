// Package httputil provides HTTP response writing helpers that enforce a
// consistent JSON envelope format across all API endpoints.
package httputil

import (
	"encoding/json"
	"net/http"

	"github.com/kumori-sh/spacetrk/pkg/exception"
)

// successResponse is the standard JSON wrapper for successful responses.
//
//	{"message": "...", "status_code": 200, "data": <payload>}
type successResponse struct {
	Message    string `json:"message"`
	StatusCode int    `json:"status_code"`
	Data       any    `json:"data,omitempty"`
}

// errorResponse is the standard JSON wrapper for error responses.
//
//	{"message": "...", "status_code": 400}
type errorResponse struct {
	Message    string `json:"message"`
	StatusCode int    `json:"status_code"`
}

// WriteJSON encodes a success response with the given message, HTTP status
// code, and data payload.
func WriteJSON(w http.ResponseWriter, status int, message string, data any) {
	encode(w, status, successResponse{Message: message, StatusCode: status, Data: data})
}

// WriteError translates any error into an appropriate JSON error response.
// Unknown errors are mapped to 500 Internal Server Error.
func WriteError(w http.ResponseWriter, err error) {
	appErr := exception.FromError(err)
	encode(w, appErr.StatusCode, errorResponse{Message: appErr.Message, StatusCode: appErr.StatusCode})
}

// NoContent sends a 204 No Content response.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// Created sends a 201 Created response with the given message and data payload.
func Created(w http.ResponseWriter, message string, data any) {
	WriteJSON(w, http.StatusCreated, message, data)
}

func encode(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}
