package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/admin"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

// ---------------------------------------------------------------------------
// Input validation
// ---------------------------------------------------------------------------

func TestValidFlagID(t *testing.T) {
	tests := []struct {
		id    string
		valid bool
	}{
		{"notifications/1", true},
		{"billing/42", true},
		{"my_feature/1/2", true},
		{"A/1", true},
		{"", false},
		{"noslash", false},
		{"/leading", false},
		{"123/abc", false},       // feature must start with letter
		{"feat/", false},         // no trailing slash
		{"feat/-1", false},       // no negative
		{"../etc/passwd", false}, // path traversal
		{"a b/1", false},         // spaces
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			assert.Equal(t, tt.valid, validFlagID.MatchString(tt.id))
		})
	}
}

func TestParseStateString(t *testing.T) {
	tests := []struct {
		input string
		want  pbflagsv1.State
	}{
		{"ENABLED", pbflagsv1.State_STATE_ENABLED},
		{"enabled", pbflagsv1.State_STATE_ENABLED},
		{"DEFAULT", pbflagsv1.State_STATE_DEFAULT},
		{"KILLED", pbflagsv1.State_STATE_KILLED},
		{"", pbflagsv1.State_STATE_UNSPECIFIED},
		{"bogus", pbflagsv1.State_STATE_UNSPECIFIED},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, parseStateString(tt.input))
		})
	}
}

func TestParseFlagValue(t *testing.T) {
	tests := []struct {
		flagType string
		raw      string
		wantErr  bool
	}{
		{"BOOL", "true", false},
		{"BOOL", "false", false},
		{"BOOL", "notabool", true},
		{"STRING", "hello", false},
		{"INT64", "42", false},
		{"INT64", "abc", true},
		{"DOUBLE", "3.14", false},
		{"DOUBLE", "xyz", true},
		{"UNKNOWN_TYPE", "val", true},
		// List types
		{"STRING_LIST", "a\nb\nc", false},
		{"INT64_LIST", "1\n5\n30", false},
		{"INT64_LIST", "abc", false}, // silently drops invalid
		{"DOUBLE_LIST", "1.5\n2.5", false},
		{"BOOL_LIST", "true\nfalse", false},
		{"STRING_LIST", "", false}, // empty list
	}
	for _, tt := range tests {
		t.Run(tt.flagType+"/"+tt.raw, func(t *testing.T) {
			_, err := parseFlagValue(tt.flagType, tt.raw)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CSRF middleware
// ---------------------------------------------------------------------------

func TestCSRFProtection(t *testing.T) {
	h := &Handler{}

	t.Run("GET sets cookie", func(t *testing.T) {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		srv := httptest.NewServer(h.csrfProtect(inner))
		defer srv.Close()

		resp, err := http.Get(srv.URL)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var found bool
		for _, c := range resp.Cookies() {
			if c.Name == csrfCookieName {
				found = true
				assert.NotEmpty(t, c.Value)
			}
		}
		assert.True(t, found, "CSRF cookie should be set on GET")
	})

	t.Run("POST without token returns 403", func(t *testing.T) {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		srv := httptest.NewServer(h.csrfProtect(inner))
		defer srv.Close()

		resp, err := http.Post(srv.URL, "application/x-www-form-urlencoded", nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("POST with matching header passes", func(t *testing.T) {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		srv := httptest.NewServer(h.csrfProtect(inner))
		defer srv.Close()

		token := "test-csrf-token"
		req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
		req.Header.Set(csrfHeaderName, token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("POST with matching form field passes", func(t *testing.T) {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		srv := httptest.NewServer(h.csrfProtect(inner))
		defer srv.Close()

		token := "test-csrf-token"
		form := url.Values{csrfFormField: {token}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("POST with mismatched token returns 403", func(t *testing.T) {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		srv := httptest.NewServer(h.csrfProtect(inner))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "real-token"})
		req.Header.Set(csrfHeaderName, "wrong-token")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("DELETE without token returns 403", func(t *testing.T) {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		srv := httptest.NewServer(h.csrfProtect(inner))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodDelete, srv.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestGenerateCSRFToken(t *testing.T) {
	t1 := generateCSRFToken()
	t2 := generateCSRFToken()
	assert.Len(t, t1, csrfTokenLen*2) // hex-encoded
	assert.NotEqual(t, t1, t2)
}

// ---------------------------------------------------------------------------
// Template helpers
// ---------------------------------------------------------------------------

func TestFormatFlagValue(t *testing.T) {
	assert.Equal(t, "—", formatFlagValue(nil))
	assert.Equal(t, "true", formatFlagValue(&pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true},
	}))
	assert.Equal(t, "hello", formatFlagValue(&pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_StringValue{StringValue: "hello"},
	}))
	assert.Equal(t, "42", formatFlagValue(&pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 42},
	}))
	assert.Equal(t, "3.14", formatFlagValue(&pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: 3.14},
	}))
}

func TestStateClass(t *testing.T) {
	assert.Equal(t, "state-enabled", stateClass(pbflagsv1.State_STATE_ENABLED))
	assert.Equal(t, "state-killed", stateClass(pbflagsv1.State_STATE_KILLED))
	assert.Equal(t, "state-unknown", stateClass(pbflagsv1.State_STATE_UNSPECIFIED))
}

func TestStateLabel(t *testing.T) {
	assert.Equal(t, "ENABLED", stateLabel(pbflagsv1.State_STATE_ENABLED))
	assert.Equal(t, "DEFAULT", stateLabel(pbflagsv1.State_STATE_DEFAULT))
	assert.Equal(t, "KILLED", stateLabel(pbflagsv1.State_STATE_KILLED))
	assert.Equal(t, "UNKNOWN", stateLabel(pbflagsv1.State_STATE_UNSPECIFIED))
}

func TestFlagIDEscape(t *testing.T) {
	assert.Equal(t, "notifications-1", flagIDEscape("notifications/1"))
	assert.Equal(t, "a-b-c", flagIDEscape("a/b/c"))
}

func TestDict(t *testing.T) {
	m := dict("a", 1, "b", "hello")
	assert.Equal(t, 1, m["a"])
	assert.Equal(t, "hello", m["b"])
}

// ---------------------------------------------------------------------------
// Handler-level validation (bad flag ID → 400)
// ---------------------------------------------------------------------------

func TestHandlerRegisterDoesNotPanic(t *testing.T) {
	// Regression: Go 1.22+ ServeMux panics if a "..." wildcard is not the last segment
	// (e.g. /api/flags/{flagID...}/state). Routes must place the wildcard last.
	h := &Handler{}
	mux := http.NewServeMux()
	require.NotPanics(t, func() { h.Register(mux) })
}

func TestValidEntityPathSegment(t *testing.T) {
	assert.True(t, validEntityPathSegment("user-123"))
	assert.False(t, validEntityPathSegment(""))
	assert.False(t, validEntityPathSegment("a/b"))
	assert.False(t, validEntityPathSegment("x?y"))
}

func TestHandlerFlagIDValidation(t *testing.T) {
	// Test handler methods directly via httptest.ResponseRecorder.
	// We pass invalid flag IDs that should be rejected before any store call.
	h := &Handler{}

	tests := []struct {
		name   string
		flagID string
		method string
		fn     func(http.ResponseWriter, *http.Request)
	}{
		{"flagDetail path traversal", "../etc/passwd", http.MethodGet, h.flagDetail},
		{"flagDetail no slash", "noslash", http.MethodGet, h.flagDetail},
		{"updateFlagState path traversal", "../evil", http.MethodPost, h.updateFlagState},
		{"updateFlagState no slash", "noslash", http.MethodPost, h.updateFlagState},
		{"setConditionOverride path traversal", "../evil", http.MethodPost, h.setConditionOverride},
		{"clearConditionOverride no slash", "noslash", http.MethodPost, h.clearConditionOverride},
		{"clearAllConditionOverrides path traversal", "../evil", http.MethodPost, h.clearAllConditionOverrides},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/test", nil)
			req.SetPathValue("flagID", tt.flagID)
			w := httptest.NewRecorder()

			tt.fn(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// Override / lock helpers (pb-wff.11/.12/.13)
// ---------------------------------------------------------------------------

func TestParseCondIndex(t *testing.T) {
	tests := []struct {
		in      string
		want    *int32
		wantErr bool
	}{
		{"", nil, false},
		{"default", nil, false},
		{"0", ptr(int32(0)), false},
		{"5", ptr(int32(5)), false},
		{"abc", nil, true},
		{"-1", ptr(int32(-1)), false}, // server still validates against chain length downstream
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseCondIndex(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.want == nil {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				assert.Equal(t, *tt.want, *got)
			}
		})
	}
}

func TestOverrideKey(t *testing.T) {
	assert.Equal(t, "default", overrideKey(nil))
	assert.Equal(t, "0", overrideKey(ptr(int32(0))))
	assert.Equal(t, "42", overrideKey(ptr(int32(42))))
}

func TestGateRuntimeOverridesDisabled(t *testing.T) {
	h := &Handler{env: EnvConfig{AllowRuntimeOverrides: false}}
	called := false
	gated := h.gateRuntimeOverrides(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodPost, "/api/lock", nil)
	w := httptest.NewRecorder()
	gated(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, called, "downstream handler should not be invoked when gated off")
	assert.Contains(t, w.Body.String(), "--allow-runtime-overrides")
}

func TestGateRuntimeOverridesEnabled(t *testing.T) {
	h := &Handler{env: EnvConfig{AllowRuntimeOverrides: true}}
	called := false
	gated := h.gateRuntimeOverrides(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/lock", nil)
	w := httptest.NewRecorder()
	gated(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, called)
}

func ptr[T any](v T) *T { return &v }

// ---------------------------------------------------------------------------
// List flag support
// ---------------------------------------------------------------------------

func TestParseFlagValueListValues(t *testing.T) {
	t.Run("STRING_LIST values", func(t *testing.T) {
		v, err := parseFlagValue("STRING_LIST", "ops@example.com\nalerts@example.com")
		require.NoError(t, err)
		sl := v.GetStringListValue()
		require.NotNil(t, sl)
		assert.Equal(t, []string{"ops@example.com", "alerts@example.com"}, sl.Values)
	})

	t.Run("INT64_LIST values", func(t *testing.T) {
		v, err := parseFlagValue("INT64_LIST", "1\n5\n30")
		require.NoError(t, err)
		il := v.GetInt64ListValue()
		require.NotNil(t, il)
		assert.Equal(t, []int64{1, 5, 30}, il.Values)
	})

	t.Run("INT64_LIST drops invalid entries", func(t *testing.T) {
		v, err := parseFlagValue("INT64_LIST", "1\nbad\n30")
		require.NoError(t, err)
		il := v.GetInt64ListValue()
		require.NotNil(t, il)
		assert.Equal(t, []int64{1, 30}, il.Values)
	})

	t.Run("STRING_LIST trims empty lines", func(t *testing.T) {
		v, err := parseFlagValue("STRING_LIST", "a\n\nb\n\n")
		require.NoError(t, err)
		sl := v.GetStringListValue()
		require.NotNil(t, sl)
		assert.Equal(t, []string{"a", "b"}, sl.Values)
	})
}

func TestFormatFlagValueList(t *testing.T) {
	t.Run("string list", func(t *testing.T) {
		v := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
			StringListValue: &pbflagsv1.StringList{Values: []string{"a", "b"}},
		}}
		assert.Equal(t, "[a, b]", formatFlagValue(v))
	})

	t.Run("int64 list", func(t *testing.T) {
		v := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{
			Int64ListValue: &pbflagsv1.Int64List{Values: []int64{1, 5, 30}},
		}}
		assert.Equal(t, "[1, 5, 30]", formatFlagValue(v))
	})

	t.Run("empty string list", func(t *testing.T) {
		v := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
			StringListValue: &pbflagsv1.StringList{Values: []string{}},
		}}
		assert.Equal(t, "[]", formatFlagValue(v))
	})

	t.Run("double list", func(t *testing.T) {
		v := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{
			DoubleListValue: &pbflagsv1.DoubleList{Values: []float64{1.5, 2.5}},
		}}
		assert.Equal(t, "[1.5, 2.5]", formatFlagValue(v))
	})
}

func TestTypeLabelList(t *testing.T) {
	assert.Equal(t, "string[]", typeLabel(pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST))
	assert.Equal(t, "int64[]", typeLabel(pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST))
	assert.Equal(t, "double[]", typeLabel(pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST))
	assert.Equal(t, "bool[]", typeLabel(pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST))
}

func TestIsListType(t *testing.T) {
	assert.True(t, isListType(pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST))
	assert.True(t, isListType(pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST))
	assert.True(t, isListType(pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST))
	assert.True(t, isListType(pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST))
	assert.False(t, isListType(pbflagsv1.FlagType_FLAG_TYPE_BOOL))
	assert.False(t, isListType(pbflagsv1.FlagType_FLAG_TYPE_STRING))
	assert.False(t, isListType(pbflagsv1.FlagType_FLAG_TYPE_INT64))
	assert.False(t, isListType(pbflagsv1.FlagType_FLAG_TYPE_DOUBLE))
}

func TestListItems(t *testing.T) {
	tests := []struct {
		name string
		val  *pbflagsv1.FlagValue
		want []string
	}{
		{"nil", nil, nil},
		{"string list", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
			StringListValue: &pbflagsv1.StringList{Values: []string{"a", "b", "c"}},
		}}, []string{"a", "b", "c"}},
		{"int64 list", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{
			Int64ListValue: &pbflagsv1.Int64List{Values: []int64{10, 20}},
		}}, []string{"10", "20"}},
		{"double list", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{
			DoubleListValue: &pbflagsv1.DoubleList{Values: []float64{1.5, 2.5}},
		}}, []string{"1.5", "2.5"}},
		{"bool list", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{
			BoolListValue: &pbflagsv1.BoolList{Values: []bool{true, false}},
		}}, []string{"true", "false"}},
		{"empty string list", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
			StringListValue: &pbflagsv1.StringList{Values: nil},
		}}, nil},
		{"scalar falls through", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, listItems(tc.val))
		})
	}
}

func TestFormatListForTextarea(t *testing.T) {
	val := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
		StringListValue: &pbflagsv1.StringList{Values: []string{"alpha", "bravo", "charlie"}},
	}}
	assert.Equal(t, "alpha\nbravo\ncharlie", formatListForTextarea(val))
	assert.Equal(t, "", formatListForTextarea(nil))
}

// ---------------------------------------------------------------------------
// Top-level /launches page
// ---------------------------------------------------------------------------

func TestLaunchesPage(t *testing.T) {
	t.Parallel()
	_, pool := testdb.Require(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	store := admin.NewStore(pool, logger)

	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{{FlagType: "BOOL"}})
	launchID := "test-launches-page-" + tf.FeatureID
	_, err := pool.Exec(context.Background(), `
		INSERT INTO feature_flags.launches
			(launch_id, scope_feature_id, dimension, ramp_percentage, affected_features, status)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE')`,
		launchID, tf.FeatureID, "user_id", 25, []string{tf.FeatureID})
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM feature_flags.launches WHERE launch_id = $1`, launchID)
	})

	h, err := NewHandler(store, logger)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/launches", nil)
	w := httptest.NewRecorder()
	h.launches(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "<h2>Launches</h2>")
	assert.Contains(t, body, launchID)
	assert.Contains(t, body, "user_id")
}
