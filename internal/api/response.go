// Package api is the HTTP layer: routing, request decoding, permission
// middleware and JSON responses. Business rules live in the packages it calls.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

// maxJSONBody caps a JSON request body. File uploads go through a different
// path with its own, much larger, limit.
const maxJSONBody = 4 << 20

// ErrorBody is the shape of every non-2xx response, so the frontend has one
// error type to handle.
type ErrorBody struct {
	Error   string            `json:"error"`
	Code    string            `json:"code,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
	TraceID string            `json:"traceId,omitempty"`
}

// APIError carries an HTTP status alongside a message safe to show a user.
type APIError struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
	// Cause is logged but never sent to the client.
	Cause error
}

func (e *APIError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *APIError) Unwrap() error { return e.Cause }

func badRequest(format string, args ...any) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: fmt.Sprintf(format, args...)}
}

func invalidFields(fields map[string]string) *APIError {
	return &APIError{
		Status:  http.StatusUnprocessableEntity,
		Code:    "validation_failed",
		Message: "Some fields need attention.",
		Fields:  fields,
	}
}

func unauthorized(message string) *APIError {
	if message == "" {
		message = "Sign in to continue."
	}
	return &APIError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: message}
}

func forbidden(message string) *APIError {
	if message == "" {
		message = "You do not have permission to do that."
	}
	return &APIError{Status: http.StatusForbidden, Code: "forbidden", Message: message}
}

func notFound(what string) *APIError {
	return &APIError{Status: http.StatusNotFound, Code: "not_found", Message: what + " was not found."}
}

func conflict(message string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: "conflict", Message: message}
}

func internal(cause error) *APIError {
	return &APIError{
		Status:  http.StatusInternalServerError,
		Code:    "internal",
		Message: "Something went wrong on the server.",
		Cause:   cause,
	}
}

// handlerFunc is the internal handler signature. Returning an error instead of
// writing one keeps every handler's exit path uniform.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// wrap adapts a handlerFunc to http.Handler, turning returned errors into
// responses and logging the ones that indicate a server fault.
func (s *Server) wrap(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			s.writeError(w, r, err)
		}
	}
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	apiErr := asAPIError(err)

	if apiErr.Status >= 500 {
		s.log.Error("request failed",
			"method", r.Method, "path", r.URL.Path,
			"status", apiErr.Status, "error", apiErr.Error())
	} else {
		s.log.Debug("request rejected",
			"method", r.Method, "path", r.URL.Path,
			"status", apiErr.Status, "code", apiErr.Code, "message", apiErr.Message)
	}

	writeJSON(w, apiErr.Status, ErrorBody{
		Error:  apiErr.Message,
		Code:   apiErr.Code,
		Fields: apiErr.Fields,
	})
}

// asAPIError maps the sentinel errors the lower layers return onto statuses,
// so handlers can return a store error directly when there is nothing to add.
func asAPIError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, store.ErrNotFound):
		return notFound("That item")
	case errors.Is(err, store.ErrDuplicate):
		return conflict("That already exists.")
	case errors.Is(err, store.ErrForeignKey):
		return conflict("Something else still refers to that item.")
	case errors.Is(err, auth.ErrInvalidCredentials):
		return unauthorized("Incorrect email or password.")
	case errors.Is(err, auth.ErrAccountDisabled):
		return forbidden("That account has been disabled.")
	case errors.Is(err, auth.ErrWeakPassword):
		return invalidFields(map[string]string{"password": strings.TrimPrefix(err.Error(), "password is too weak: ")})
	case errors.Is(err, auth.ErrEmailTaken):
		return invalidFields(map[string]string{"email": "An account with that email already exists."})
	case errors.Is(err, auth.ErrInvalidEmail):
		return invalidFields(map[string]string{"email": "That does not look like an email address."})
	case errors.Is(err, auth.ErrRegistrationClosed):
		return forbidden("Registration is disabled on this server. Ask an administrator for an account.")
	}

	return internal(err)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if body == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	// The header is already committed, so a late encoding failure can only be
	// logged, not converted into an error response.
	_ = enc.Encode(body)
}

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// decodeJSON reads a JSON body into dst, rejecting unknown fields so a typo in
// a client request surfaces immediately instead of being silently ignored.
func decodeJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mediaType, _, _ := strings.Cut(ct, ";")
		if strings.TrimSpace(mediaType) != "application/json" {
			return badRequest("Expected a JSON request body.")
		}
	}

	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError

		switch {
		case errors.As(err, &syntaxErr):
			return badRequest("The request body is not valid JSON (at byte %d).", syntaxErr.Offset)
		case errors.As(err, &typeErr):
			return invalidFields(map[string]string{typeErr.Field: "Expected a " + typeErr.Type.String() + "."})
		case strings.Contains(err.Error(), "unknown field"):
			return badRequest("%s", strings.ToUpper(err.Error()[:1])+err.Error()[1:]+".")
		case err.Error() == "http: request body too large":
			return badRequest("The request body is too large.")
		default:
			return badRequest("The request body could not be read.")
		}
	}

	// A second value in the body usually means a client bug worth reporting.
	if dec.More() {
		return badRequest("The request body must contain a single JSON object.")
	}
	return nil
}
