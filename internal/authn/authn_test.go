package authn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNone(t *testing.T) {
	auth := None{}
	r := httptest.NewRequest("GET", "/", nil)
	id, err := auth.Authenticate(r)
	require.NoError(t, err)
	assert.Equal(t, "anonymous", id.Subject)
}

func TestSharedSecret(t *testing.T) {
	auth, err := NewSharedSecret("s3cret")
	require.NoError(t, err)

	t.Run("valid token", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer s3cret")
		id, err := auth.Authenticate(r)
		require.NoError(t, err)
		assert.Equal(t, "api", id.Subject)
	})

	t.Run("valid token with X-Actor", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer s3cret")
		r.Header.Set("X-Actor", "alice@example.com")
		id, err := auth.Authenticate(r)
		require.NoError(t, err)
		assert.Equal(t, "alice@example.com", id.Subject)
	})

	t.Run("missing header", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		_, err := auth.Authenticate(r)
		assert.Error(t, err)
	})

	t.Run("wrong scheme", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		_, err := auth.Authenticate(r)
		assert.Error(t, err)
	})

	t.Run("wrong token", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer wrong")
		_, err := auth.Authenticate(r)
		assert.Error(t, err)
	})

	t.Run("empty token rejected", func(t *testing.T) {
		_, err := NewSharedSecret("")
		assert.Error(t, err)
	})
}

func TestTrustedHeader(t *testing.T) {
	t.Run("default header", func(t *testing.T) {
		auth := NewTrustedHeader("")
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Forwarded-User", "bob@example.com")
		id, err := auth.Authenticate(r)
		require.NoError(t, err)
		assert.Equal(t, "bob@example.com", id.Subject)
	})

	t.Run("custom header", func(t *testing.T) {
		auth := NewTrustedHeader("X-Remote-User")
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Remote-User", "charlie")
		id, err := auth.Authenticate(r)
		require.NoError(t, err)
		assert.Equal(t, "charlie", id.Subject)
	})

	t.Run("missing header", func(t *testing.T) {
		auth := NewTrustedHeader("")
		r := httptest.NewRequest("GET", "/", nil)
		_, err := auth.Authenticate(r)
		assert.Error(t, err)
	})
}

func TestMiddleware(t *testing.T) {
	t.Run("success stores identity in context", func(t *testing.T) {
		var gotSubject string
		inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			gotSubject = SubjectFromContext(r.Context(), "fallback")
		})

		handler := Middleware(None{}, inner)
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil)
		handler.ServeHTTP(w, r)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "anonymous", gotSubject)
	})

	t.Run("failure returns 401", func(t *testing.T) {
		auth, err := NewSharedSecret("tok")
		require.NoError(t, err)

		inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("handler should not be called")
		})

		handler := Middleware(auth, inner)
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil)
		// no Authorization header
		handler.ServeHTTP(w, r)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestNewAuthenticator(t *testing.T) {
	t.Run("empty strategy defaults to none", func(t *testing.T) {
		a, err := NewAuthenticator(Config{})
		require.NoError(t, err)
		assert.IsType(t, None{}, a)
	})

	t.Run("none", func(t *testing.T) {
		a, err := NewAuthenticator(Config{Strategy: "none"})
		require.NoError(t, err)
		assert.IsType(t, None{}, a)
	})

	t.Run("shared-secret", func(t *testing.T) {
		a, err := NewAuthenticator(Config{Strategy: "shared-secret", Token: "tok"})
		require.NoError(t, err)
		assert.IsType(t, &SharedSecret{}, a)
	})

	t.Run("shared-secret missing token", func(t *testing.T) {
		_, err := NewAuthenticator(Config{Strategy: "shared-secret"})
		assert.Error(t, err)
	})

	t.Run("trusted-header", func(t *testing.T) {
		a, err := NewAuthenticator(Config{Strategy: "trusted-header"})
		require.NoError(t, err)
		assert.IsType(t, &TrustedHeader{}, a)
	})

	t.Run("unknown strategy", func(t *testing.T) {
		_, err := NewAuthenticator(Config{Strategy: "magic"})
		assert.Error(t, err)
	})
}

func TestSubjectFromContext(t *testing.T) {
	t.Run("no identity returns fallback", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		assert.Equal(t, "fallback", SubjectFromContext(r.Context(), "fallback"))
	})

	t.Run("empty subject returns fallback", func(t *testing.T) {
		// Simulate a zero Identity stored in context — shouldn't happen in
		// practice, but exercise the guard.
		ctx := context.WithValue(context.Background(), ctxKey{}, Identity{})
		assert.Equal(t, "fallback", SubjectFromContext(ctx, "fallback"))
	})
}
