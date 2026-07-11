package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

type Code string

const (
	CodeInvalid      Code = "invalid_request"
	CodeUnauthorized Code = "unauthorized"
	CodeForbidden    Code = "forbidden"
	CodeNotFound     Code = "not_found"
	CodeConflict     Code = "conflict"
	CodeUpstream     Code = "upstream_error"
	CodeUnavailable  Code = "unavailable"
	CodeInternal     Code = "internal"
	CodeTooMany      Code = "too_many_requests"
	CodeNotReady     Code = "not_ready"
)

type Error struct {
	Code       Code
	Message    string
	HTTPStatus int
	Cause      error
	Public     bool
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

func New(code Code, status int, message string) *Error {
	return &Error{Code: code, HTTPStatus: status, Message: message, Public: true}
}

func Wrap(code Code, status int, message string, cause error) *Error {
	return &Error{Code: code, HTTPStatus: status, Message: message, Cause: cause, Public: true}
}

func Internal(cause error) *Error {
	return &Error{Code: CodeInternal, HTTPStatus: http.StatusInternalServerError, Message: "internal server error", Cause: cause, Public: false}
}

func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

func PublicMessage(err error) (Code, int, string) {
	if e, ok := As(err); ok {
		msg := e.Message
		if !e.Public {
			msg = "internal server error"
		}
		return e.Code, e.HTTPStatus, msg
	}
	return CodeInternal, http.StatusInternalServerError, "internal server error"
}

var (
	ErrNotFound     = New(CodeNotFound, http.StatusNotFound, "not found")
	ErrUnauthorized = New(CodeUnauthorized, http.StatusUnauthorized, "unauthorized")
	ErrForbidden    = New(CodeForbidden, http.StatusForbidden, "forbidden")
	ErrInvalid      = New(CodeInvalid, http.StatusBadRequest, "invalid request")
	ErrTooMany      = New(CodeTooMany, http.StatusTooManyRequests, "too many requests")
	ErrUnavailable  = New(CodeUnavailable, http.StatusServiceUnavailable, "service unavailable")
)
