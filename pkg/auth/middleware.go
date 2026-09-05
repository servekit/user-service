// Edge HTTP middleware: verify the bearer session at the gateway and reject
// unauthorized requests before they reach any backend handler.
package auth

import (
	"net/http"
	"strconv"
	"strings"
)

// Middleware verifies bearer sessions at the HTTP edge. Construct once with
// NewMiddleware and wrap the gateway's handler with Wrap.
type Middleware struct {
	sessions       SessionService
	publicPaths    map[string]struct{}
	publicPrefixes []string
}

// MiddlewareOption configures a Middleware.
type MiddlewareOption func(*Middleware)

// WithPublicPaths marks exact request paths (e.g. "/api/v1/auth/login") that
// skip verification: pre-login routes and routes carrying their own
// credentials (HMAC-ingest, metrics). They still pass through header
// stripping — inbound identity headers never survive this middleware.
func WithPublicPaths(paths ...string) MiddlewareOption {
	return func(m *Middleware) {
		for _, p := range paths {
			m.publicPaths[p] = struct{}{}
		}
	}
}

// WithPublicPrefixes marks path prefixes (e.g. "/v1/e/") that skip
// verification. Use for public routes with path parameters, where exact
// matching cannot work.
func WithPublicPrefixes(prefixes ...string) MiddlewareOption {
	return func(m *Middleware) {
		m.publicPrefixes = append(m.publicPrefixes, prefixes...)
	}
}

// NewMiddleware constructs a Middleware. sessions must be non-nil; a nil
// dependency is a wiring bug in the assembler, not a runtime condition to
// mask.
func NewMiddleware(sessions SessionService, opts ...MiddlewareOption) *Middleware {
	m := &Middleware{
		sessions:    sessions,
		publicPaths: make(map[string]struct{}),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Wrap returns the edge middleware around next. Per request: strip inbound
// identity headers (spoofing defense), pass CORS preflights and public paths
// through, otherwise verify the bearer session — a failure answers 401 here
// without invoking next; a success rewrites X-UserID/X-SessionID and attaches
// the identity to the request context before delegating.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Identity headers are this middleware's output only. Drop inbound
		// copies unconditionally — both the plain and the Grpc-Metadata-
		// wire form, including on public paths — so a client can never
		// smuggle an identity past the edge.
		r.Header.Del(XUserID)
		r.Header.Del(XSessionID)
		r.Header.Del(xUserHeader)
		r.Header.Del(xSessionHeader)

		// Browser CORS preflights carry no credentials; answering them with
		// 401 would break legitimate cross-origin API use.
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if m.isPublic(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := verifyBearer(r.Context(), m.sessions, token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		r.Header.Set(xUserHeader, strconv.FormatInt(id.UserID, 10))
		r.Header.Set(xSessionHeader, id.SessionID)
		next.ServeHTTP(w, r.WithContext(annotateCtx(r.Context(), id)))
	})
}

// isPublic reports whether the path skips session verification.
func (m *Middleware) isPublic(path string) bool {
	if _, ok := m.publicPaths[path]; ok {
		return true
	}
	for _, p := range m.publicPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// bearerToken extracts the bearer credential from an Authorization header
// value. The scheme match is case-insensitive (RFC 9110); the token must be
// non-empty.
func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") || token == "" {
		return "", false
	}
	return token, true
}
