package apperror

import (
	"errors"
	"fmt"
)

// AppError represents a structured application error with HTTP status code,
// 5-digit business error code, and user-facing message.
type AppError struct {
	BizCode    int // 5-digit business code (e.g. 40100, 40301)
	Message    string
	StatusCode int
	Cause      error
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%d] %s: %v", e.BizCode, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%d] %s", e.BizCode, e.Message)
}

func (e *AppError) Unwrap() error { return e.Cause }

func New(bizCode, statusCode int, message string) *AppError {
	return &AppError{BizCode: bizCode, Message: message, StatusCode: statusCode}
}

func Wrap(err error, bizCode, statusCode int, message string) *AppError {
	return &AppError{BizCode: bizCode, Message: message, StatusCode: statusCode, Cause: err}
}

func (e *AppError) WithMessage(message string) *AppError {
	return &AppError{BizCode: e.BizCode, Message: message, StatusCode: e.StatusCode, Cause: e.Cause}
}

// 5-digit business code convention: first 3 digits = HTTP status, last 2 = sequence.
var (
	ErrNotFound     = New(40401, 404, "resource not found")
	ErrInvalidInput = New(40001, 400, "invalid input")
	ErrUnauthorized = New(40100, 401, "unauthorized")
	ErrForbidden    = New(40301, 403, "forbidden")
	ErrConflict     = New(40901, 409, "resource conflict")
	ErrInternal     = New(50000, 500, "internal error")
	ErrUnavailable  = New(50301, 503, "service unavailable")
)

// VMS domain errors
var (
	ErrCameraNotFound       = ErrNotFound.WithMessage("camera not found")
	ErrRecordingNotFound    = ErrNotFound.WithMessage("recording not found")
	ErrScheduleNotFound     = ErrNotFound.WithMessage("recording schedule not found")
	ErrMediaMTXUnreachable  = ErrUnavailable.WithMessage("mediamtx server unreachable")
	ErrStreamURLInvalid     = ErrInvalidInput.WithMessage("invalid stream URL")
	ErrONVIFDiscoveryFailed = ErrInternal.WithMessage("ONVIF discovery failed")
)

func IsNotFound(err error) bool {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.StatusCode == 404
	}
	return false
}
