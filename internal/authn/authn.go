// Package authn provides pluggable authentication middleware for the pbflags
// admin API. It extracts an [Identity] from each HTTP request using a
// configurable [Authenticator] strategy.
//
// Strategies are selected via [Config.Strategy]:
//
//   - "none"           — no authentication; all requests are anonymous (default)
//   - "shared-secret"  — Bearer token matched against a configured secret
//   - "trusted-header" — identity read from a reverse-proxy header (e.g. X-Forwarded-User)
package authn

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Identity represents an authenticated caller.
type Identity struct {
	Subject string // who, e.g. "alice@example.com", "ci-bot"
}

type ctxKey struct{}

// FromContext returns the Identity attached to ctx, or a zero Identity if none.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// SubjectFromContext is a convenience that returns the subject string, or
// fallback if no identity is present.
func SubjectFromContext(ctx context.Context, fallback string) string {
	if id, ok := FromContext(ctx); ok && id.Subject != "" {
		return id.Subject
	}
	return fallback
}

// Authenticator extracts an identity from an HTTP request.
type Authenticator interface {
	// Authenticate inspects the request and returns the caller's identity.
	// Returning an error rejects the request with 401.
	Authenticate(r *http.Request) (Identity, error)
}

// Middleware wraps an http.Handler, running the Authenticator on every request.
// On success the Identity is stored in the request context. On failure a 401
// response is returned.
func Middleware(auth Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := auth.Authenticate(r)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ── Strategies ──────────────────────────────────────────────────────────────

// None always succeeds with Subject "anonymous".
type None struct{}

func (None) Authenticate(_ *http.Request) (Identity, error) {
	return Identity{Subject: "anonymous"}, nil
}

// SharedSecret validates a Bearer token against a pre-shared secret using
// constant-time comparison.
type SharedSecret struct {
	token []byte
}

// NewSharedSecret creates a SharedSecret authenticator. The token must not be
// empty.
func NewSharedSecret(token string) (*SharedSecret, error) {
	if token == "" {
		return nil, fmt.Errorf("authn: shared-secret token must not be empty")
	}
	return &SharedSecret{token: []byte(token)}, nil
}

func (s *SharedSecret) Authenticate(r *http.Request) (Identity, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return Identity{}, fmt.Errorf("missing Authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return Identity{}, fmt.Errorf("Authorization header must use Bearer scheme")
	}
	got := []byte(strings.TrimPrefix(auth, prefix))
	if subtle.ConstantTimeCompare(got, s.token) != 1 {
		return Identity{}, fmt.Errorf("invalid token")
	}
	// Shared-secret callers are identified by an optional X-Actor header,
	// falling back to "api" if not provided. This lets CLI and automation
	// callers declare who they are for audit logging.
	subject := r.Header.Get("X-Actor")
	if subject == "" {
		subject = "api"
	}
	return Identity{Subject: subject}, nil
}

// TrustedHeader reads identity from a header set by a reverse proxy. The
// header name is configurable (defaults to "X-Forwarded-User").
type TrustedHeader struct {
	header string
}

// NewTrustedHeader creates a TrustedHeader authenticator. If header is empty,
// "X-Forwarded-User" is used.
func NewTrustedHeader(header string) *TrustedHeader {
	if header == "" {
		header = "X-Forwarded-User"
	}
	return &TrustedHeader{header: header}
}

func (t *TrustedHeader) Authenticate(r *http.Request) (Identity, error) {
	subject := r.Header.Get(t.header)
	if subject == "" {
		return Identity{}, fmt.Errorf("missing %s header", t.header)
	}
	return Identity{Subject: subject}, nil
}

// ── Config + factory ────────────────────────────────────────────────────────

// Config holds authentication configuration.
type Config struct {
	Strategy string // "none", "shared-secret", "trusted-header"
	Token    string // shared-secret token
	Header   string // trusted-header header name
}

// LoadConfig reads auth configuration from environment variables.
func LoadConfig() Config {
	return Config{
		Strategy: os.Getenv("PBFLAGS_AUTH_STRATEGY"),
		Token:    os.Getenv("PBFLAGS_AUTH_TOKEN"),
		Header:   os.Getenv("PBFLAGS_AUTH_HEADER"),
	}
}

// NewAuthenticator creates an Authenticator from the given Config.
func NewAuthenticator(cfg Config) (Authenticator, error) {
	switch cfg.Strategy {
	case "", "none":
		return None{}, nil
	case "shared-secret":
		return NewSharedSecret(cfg.Token)
	case "trusted-header":
		return NewTrustedHeader(cfg.Header), nil
	default:
		return nil, fmt.Errorf("authn: unknown strategy %q (want none, shared-secret, or trusted-header)", cfg.Strategy)
	}
}
