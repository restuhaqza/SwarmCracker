// Package output owns the CLI's machine-readable output modes and the mapping
// from failures to stable error codes and exit statuses.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
)

// Format is a CLI output mode.
type Format string

const (
	FormatTable  Format = "table"
	FormatJSON   Format = "json"
	FormatName   Format = "name"
	FormatNDJSON Format = "ndjson"
)

// ParseFormat validates a user-supplied output format.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatTable, FormatJSON, FormatName, FormatNDJSON:
		return Format(s), nil
	case "":
		return FormatTable, nil
	default:
		return "", NewErrorf(CodeUsage, "unsupported output format %q (want table, json, name or ndjson)", s)
	}
}

// Code is a stable error code. It is the contract; message text is not.
type Code string

const (
	CodeInvalid            Code = "invalid"
	CodeAmbiguous          Code = "ambiguous"
	CodeNotFound           Code = "not_found"
	CodeUnauthenticated    Code = "unauthenticated"
	CodeForbidden          Code = "forbidden"
	CodeConflict           Code = "conflict"
	CodeCursorExpired      Code = "cursor_expired"
	CodeRateLimited        Code = "rate_limited"
	CodeUnavailable        Code = "unavailable"
	CodeTimeout            Code = "timeout"
	CodeUnsupportedVersion Code = "unsupported_version"
	CodeNotImplemented     Code = "not_implemented"
	CodeInternal           Code = "internal"
	CodeUsage              Code = "usage"
	CodeUnreachable        Code = "unreachable"
)

// Error is a typed CLI/API failure. It implements error and carries the stable
// code, retry hint and exit status derived from the contract.
type Error struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"requestId,omitempty"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// NewError creates a typed error with the default retry and exit mapping.
func NewError(code Code, message string) *Error {
	return &Error{
		Code:      code,
		Message:   message,
		Retryable: retryable(code),
	}
}

// NewErrorf is NewError with formatting.
func NewErrorf(code Code, format string, args ...any) *Error {
	return NewError(code, fmt.Sprintf(format, args...))
}

// WithDetails returns e with a details map, for fluent construction.
func (e *Error) WithDetails(details map[string]any) *Error {
	e.Details = details
	return e
}

// WithRequestID returns e with a request ID.
func (e *Error) WithRequestID(id string) *Error {
	e.RequestID = id
	return e
}

// Error implements error.
func (e *Error) Error() string { return e.Message }

// ExitCode maps the code to the documented CLI exit status.
func (e *Error) ExitCode() int { return exitCode(e.Code) }

// Document converts the error to its wire form.
func (e *Error) Document() v1.ErrorDocument {
	return v1.NewErrorDocument(v1.Error{
		Code:      string(e.Code),
		Message:   e.Message,
		RequestID: e.RequestID,
		Retryable: e.Retryable,
		Details:   e.Details,
	})
}

func retryable(code Code) bool {
	switch code {
	case CodeRateLimited, CodeUnavailable, CodeTimeout, CodeInternal, CodeUnreachable:
		return true
	default:
		return false
	}
}

func exitCode(code Code) int {
	switch code {
	case CodeUsage, CodeUnsupportedVersion, CodeInvalid:
		return 2
	case CodeAmbiguous, CodeNotFound:
		return 3
	case CodeConflict:
		return 4
	case CodeUnauthenticated, CodeForbidden:
		return 5
	case CodeRateLimited, CodeUnavailable, CodeTimeout, CodeUnreachable:
		return 6
	case CodeCursorExpired:
		return 1
	default:
		return 1
	}
}

// ExitCode returns the process exit status for an error. A nil error is 0.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *Error
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return 1
}

// WriteJSON writes exactly one JSON document and a trailing newline.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteError writes a typed error document to w. An untyped error is reported
// as internal so stderr is always parseable in JSON mode.
func WriteError(w io.Writer, err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if !errors.As(err, &e) {
		e = NewError(CodeInternal, err.Error())
	}
	return WriteJSON(w, e.Document())
}
