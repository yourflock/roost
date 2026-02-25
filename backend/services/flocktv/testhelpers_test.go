// testhelpers_test.go — Test helpers shared across flocktv test files.
package flocktv

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// withChiParam injects a chi URL parameter into a request context.
// Needed for handlers that call chi.URLParam(r, "key").
func withChiParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	return r.WithContext(ctx)
}

// itoa is a convenience wrapper for strconv.Itoa.
var itoa = strconv.Itoa
