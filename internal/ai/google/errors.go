package google

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/apierrors"

	"omni-bot/internal/ai"
)

func classifyError(provider string, err error) error {
	if err == nil {
		return nil
	}
	var aiErr *ai.Error
	if errors.As(err, &aiErr) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return &ai.Error{
			Kind:     ai.ErrUnknown,
			Provider: provider,
			Message:  "request canceled",
			Err:      err,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ai.Error{
			Kind:     ai.ErrTransient,
			Provider: provider,
			Message:  "request timed out",
			Err:      err,
		}
	}

	var sdkErr *apierrors.APIError
	if errors.As(err, &sdkErr) {
		body := sdkErr.Body
		if body == "" {
			body = sdkErr.Message
		}
		return &ai.Error{
			Kind:       ai.ClassifyHTTPStatus(sdkErr.StatusCode, body),
			StatusCode: sdkErr.StatusCode,
			Provider:   provider,
			Message:    summarizeErrorMessage(body),
			RetryAfter: parseRetryAfter(sdkErr.RawResponse),
			Err:        err,
		}
	}

	var clientErr *apierrors.CreateInteractionClientError
	if errors.As(err, &clientErr) {
		status := httpStatusFromMeta(clientErr.HTTPMeta.Response, http.StatusBadRequest)
		msg := interactionErrMessage(clientErr.Error_.Message, clientErr.Error())
		return &ai.Error{
			Kind:       ai.ClassifyHTTPStatus(status, msg),
			StatusCode: status,
			Provider:   provider,
			Message:    summarizeErrorMessage(msg),
			RetryAfter: parseRetryAfter(clientErr.HTTPMeta.Response),
			Err:        err,
		}
	}

	var serverErr *apierrors.CreateInteractionServerError
	if errors.As(err, &serverErr) {
		status := httpStatusFromMeta(serverErr.HTTPMeta.Response, http.StatusInternalServerError)
		msg := interactionErrMessage(serverErr.Error_.Message, serverErr.Error())
		return &ai.Error{
			Kind:       ai.ClassifyHTTPStatus(status, msg),
			StatusCode: status,
			Provider:   provider,
			Message:    summarizeErrorMessage(msg),
			RetryAfter: parseRetryAfter(serverErr.HTTPMeta.Response),
			Err:        err,
		}
	}

	var genaiErr genai.APIError
	if errors.As(err, &genaiErr) {
		msg := genaiErr.Message
		if msg == "" {
			msg = genaiErr.Status
		}
		return &ai.Error{
			Kind:       ai.ClassifyHTTPStatus(genaiErr.Code, msg),
			StatusCode: genaiErr.Code,
			Provider:   provider,
			Message:    summarizeErrorMessage(msg),
			Err:        err,
		}
	}

	kind := ai.ErrorKindOf(err)
	if kind == "" {
		kind = ai.ErrUnknown
	}
	return &ai.Error{
		Kind:     kind,
		Provider: provider,
		Message:  summarizeErrorMessage(err.Error()),
		Err:      err,
	}
}

func httpStatusFromMeta(resp *http.Response, fallback int) int {
	if resp != nil && resp.StatusCode > 0 {
		return resp.StatusCode
	}
	return fallback
}

func interactionErrMessage(msg *string, fallback string) string {
	if msg != nil && strings.TrimSpace(*msg) != "" {
		return *msg
	}
	return fallback
}

func parseRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(raw); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func summarizeErrorMessage(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}
