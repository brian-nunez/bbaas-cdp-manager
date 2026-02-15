package errors

import "net/http"

type ErrorType string

const (
	ErrInvalidRequest      ErrorType = "INVALID_REQUEST"
	ErrUnauthorized        ErrorType = "UNAUTHORIZED"
	ErrNotFound            ErrorType = "NOT_FOUND"
	ErrNotAllowed          ErrorType = "NOT_ALLOWED"
	ErrInternalServerError ErrorType = "INTERNAL_SERVER_ERROR"
	ErrServiceUnavailable  ErrorType = "SERVICE_UNAVAILABLE"
)

type ErrorMessage struct {
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

type ErrorResponse struct {
	HTTPStatusCode int          `json:"status"`
	ErrorMessage   ErrorMessage `json:"error"`
}

type errorBuilder struct {
	httpStatusCode int
	errorCode      string
	message        string
}

func (b *errorBuilder) WithStatusCode(code int) *errorBuilder {
	b.httpStatusCode = code
	return b
}

func (b *errorBuilder) WithMessage(message string) *errorBuilder {
	b.message = message
	return b
}

func (b *errorBuilder) WithErrorCode(errorCode string) *errorBuilder {
	b.errorCode = errorCode
	return b
}

func (b *errorBuilder) Build() *ErrorResponse {
	return &ErrorResponse{
		HTTPStatusCode: b.httpStatusCode,
		ErrorMessage: ErrorMessage{
			ErrorCode:    b.errorCode,
			ErrorMessage: b.message,
		},
	}
}

func Custom() *errorBuilder {
	return &errorBuilder{}
}

func InvalidRequest() *errorBuilder {
	return &errorBuilder{
		httpStatusCode: http.StatusBadRequest,
		errorCode:      string(ErrInvalidRequest),
		message:        "Invalid Request",
	}
}

func Unauthorized() *errorBuilder {
	return &errorBuilder{
		httpStatusCode: http.StatusUnauthorized,
		errorCode:      string(ErrUnauthorized),
		message:        "Unauthorized",
	}
}

func NotFound() *errorBuilder {
	return &errorBuilder{
		httpStatusCode: http.StatusNotFound,
		errorCode:      string(ErrNotFound),
		message:        "Not Found",
	}
}

func NotAllowed() *errorBuilder {
	return &errorBuilder{
		httpStatusCode: http.StatusMethodNotAllowed,
		errorCode:      string(ErrNotAllowed),
		message:        "Not Allowed",
	}
}

func InternalServerError() *errorBuilder {
	return &errorBuilder{
		httpStatusCode: http.StatusInternalServerError,
		errorCode:      string(ErrInternalServerError),
		message:        "Internal Server Error",
	}
}

func ServiceNotAvailable() *errorBuilder {
	return &errorBuilder{
		httpStatusCode: http.StatusServiceUnavailable,
		errorCode:      string(ErrServiceUnavailable),
		message:        "Service Not Available",
	}
}

func GenerateByStatusCode(code int) *errorBuilder {
	switch code {
	case http.StatusBadRequest:
		return InvalidRequest()
	case http.StatusUnauthorized:
		return Unauthorized()
	case http.StatusNotFound:
		return NotAllowed()
	case http.StatusMethodNotAllowed:
		return NotAllowed()
	case http.StatusInternalServerError:
		return InternalServerError()
	case http.StatusServiceUnavailable:
		return ServiceNotAvailable()
	}

	return InternalServerError()
}
