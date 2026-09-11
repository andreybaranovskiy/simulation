package api

import (
	"context"
	"net/http"
	"time"
)

// contextWithTimeout bounds a piece of work inside a request without losing
// the cancellation that arrives when the client disconnects.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
