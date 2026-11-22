package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type ErrorKind string

const (
	ErrAuth           ErrorKind = "auth"
	ErrInvalidArg     ErrorKind = "invalid_arg"
	ErrRateLimit      ErrorKind = "rate_limit"
	ErrTransient      ErrorKind = "transient"
	ErrContextTooLong ErrorKind = "context_too_long"
	ErrUnknown        ErrorKind = "unknown"
)

// Error is the unified error type returned by all AI protocol adapters so the
// agent loop can decide retries explicitly without relying on SDK retries.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Provider   string
	Message    string
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("ai")
	if e.Provider != "" {
		b.WriteString(" [")
		b.WriteString(e.Provider)
		b.WriteByte(']')
	}
	if e.Kind != "" {
		b.WriteString(" (")
		b.WriteString(string(e.Kind))
		if e.StatusCode > 0 {
			fmt.Fprintf(&b, " %d", e.StatusCode)
		}
		b.WriteByte(')')
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	} else if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error {
	return e.Err
}

// IsRetryable reports whether err represents a transient failure or rate limit
// suitable for explicit retry by the agent loop. Context cancellation is never retryable.
func IsRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	kind := ErrorKindOf(err)
	return kind == ErrRateLimit || kind == ErrTransient
}

// ErrorKindOf extracts the unified ErrorKind from err, falling back to network
// error inspection for unwrapped transport errors.
func ErrorKindOf(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var aiErr *Error
	if errors.As(err, &aiErr) {
		if aiErr.Kind != "" {
			return aiErr.Kind
		}
		return ErrUnknown
	}
	if errors.Is(err, context.Canceled) {
		return ErrUnknown
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTransient
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrTransient
	}
	return ErrUnknown
}

// ClassifyHTTPStatus maps an HTTP status code and optional response body into
// a unified ErrorKind.
func ClassifyHTTPStatus(statusCode int, body string) ErrorKind {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	case http.StatusTooManyRequests:
		return ErrRateLimit
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusInternalServerError:
		return ErrTransient
	case http.StatusRequestEntityTooLarge:
		return ErrContextTooLong
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		if looksLikeContextTooLong(body) {
			return ErrContextTooLong
		}
		return ErrInvalidArg
	default:
		if statusCode >= 500 && statusCode <= 599 {
			return ErrTransient
		}
		if statusCode >= 400 && statusCode <= 499 {
			if looksLikeContextTooLong(body) {
				return ErrContextTooLong
			}
			return ErrInvalidArg
		}
		return ErrUnknown
	}
}

func looksLikeContextTooLong(body string) bool {
	lower := strings.ToLower(body)
	for _, needle := range []string{
		"context_length_exceeded",
		"context length",
		"maximum context",
		"token count exceeds",
		"exceeds the maximum number of tokens",
		"input is too long",
		"prompt is too long",
		"too many tokens",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
