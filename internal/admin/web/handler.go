// Package web provides an embedded admin dashboard for the pbflags feature
// flag system. It serves server-rendered HTML with htmx for dynamic updates,
// backed by the admin Store for persistence.
package web

import (
	"embed"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	pbflagspb "github.com/SpotlightGOV/pbflags/gen/pbflags"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/admin"
)

//go:embed assets
var assets embed.FS

// Handler serves the admin web UI.
type Handler struct {
	store    *admin.Store
	logger   *slog.Logger
	tmpl     *template.Template
	staticFS fs.FS
}

// NewHandler creates a web UI handler backed by the given store.
func NewHandler(store *admin.Store, logger *slog.Logger) (*Handler, error) {
	funcMap := template.FuncMap{
		"flagValue":    formatFlagValue,
		"stateClass":   stateClass,
		"stateLabel":   stateLabel,
		"layerLabel":   layerLabel,
		"typeLabel":    typeLabel,
		"timeAgo":      timeAgo,
		"isUserLayer":  isUserLayer,
		"json":         toJSON,
		"flagIDEscape": flagIDEscape,
		"dict": dict,
	}

	tmplFS, err := fs.Sub(assets, "assets/templates")
	if err != nil {
		return nil, fmt.Errorf("template fs: %w", err)
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseFS(tmplFS, "*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	staticFS, err := fs.Sub(assets, "assets/static")
	if err != nil {
		return nil, fmt.Errorf("static fs: %w", err)
	}

	return &Handler{store: store, logger: logger, tmpl: tmpl, staticFS: staticFS}, nil
}

// Register adds web UI routes to the given mux, wrapped with CSRF protection.
func (h *Handler) Register(mux *http.ServeMux) {
	// Internal mux for route matching; CSRF middleware wraps the whole thing.
	inner := http.NewServeMux()

	// Static assets (CSS).
	inner.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(h.staticFS))))

	// Pages.
	inner.HandleFunc("GET /{$}", h.dashboard)
	inner.HandleFunc("GET /flags/{flagID...}", h.flagDetail)
	inner.HandleFunc("GET /audit", h.auditLog)

	// htmx API endpoints. Wildcards must be the final path segment (Go 1.22+ ServeMux);
	// patterns like /api/flags/{flagID...}/state panic at registration.
	inner.HandleFunc("POST /api/flags/state/{flagID...}", h.updateFlagState)
	inner.HandleFunc("POST /api/flags/overrides/{flagID...}", h.setOverride)
	inner.HandleFunc("DELETE /api/flags/overrides/entity/{entityID}/{flagID...}", h.removeOverride)

	mux.Handle("/", h.csrfProtect(inner))
}

// ---------------------------------------------------------------------------
// CSRF protection (double-submit cookie)
// ---------------------------------------------------------------------------

const (
	csrfCookieName = "pbflags_csrf"
	csrfHeaderName = "X-CSRF-Token"
	csrfFormField  = "csrf_token"
	csrfTokenLen   = 32
)

// csrfProtect wraps a handler to enforce CSRF validation on mutating requests.
// GET/HEAD/OPTIONS requests get a token set (or refreshed); POST/PUT/DELETE
// requests must present a matching token via header or form field.
func (h *Handler) csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			h.ensureCSRFCookie(w, r)
			next.ServeHTTP(w, r)
		default:
			if !h.validCSRFToken(r) {
				http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		}
	})
}

func (h *Handler) ensureCSRFCookie(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(csrfCookieName); err == nil {
		return // already set
	}
	token := generateCSRFToken()
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false, // JS needs to read it for htmx
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) validCSRFToken(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}

	// Accept token from header (htmx) or form field (plain forms).
	token := r.Header.Get(csrfHeaderName)
	if token == "" {
		token = r.FormValue(csrfFormField)
	}

	return token != "" && token == cookie.Value
}

func generateCSRFToken() string {
	b := make([]byte, csrfTokenLen)
	if _, err := rand.Read(b); err != nil {
		panic("csrf: failed to generate random token: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Page handlers
// ---------------------------------------------------------------------------

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	features, err := h.store.ListFeatures(r.Context())
	if err != nil {
		h.serverError(w, "list features", err)
		return
	}

	data := map[string]any{
		"Features":  features,
		"Page":      "dashboard",
		"FlagCount": countFlags(features),
	}

	// htmx partial swap: return just the content block.
	if r.Header.Get("HX-Request") == "true" {
		h.render(w, "dashboard_content", data)
		return
	}
	h.render(w, "layout", data)
}

func (h *Handler) flagDetail(w http.ResponseWriter, r *http.Request) {
	flagID := r.PathValue("flagID")
	if flagID == "" {
		http.Error(w, "missing flag_id", http.StatusBadRequest)
		return
	}
	if !validFlagID.MatchString(flagID) {
		http.Error(w, "invalid flag_id format: expected feature_id/field_number", http.StatusBadRequest)
		return
	}

	flag, err := h.store.GetFlag(r.Context(), flagID)
	if err != nil {
		h.serverError(w, "get flag", err)
		return
	}
	if flag == nil {
		http.Error(w, "flag not found", http.StatusNotFound)
		return
	}

	entries, err := h.store.GetAuditLog(r.Context(), admin.AuditLogFilter{FlagID: flagID, Limit: 20})
	if err != nil {
		h.serverError(w, "get audit log", err)
		return
	}

	data := map[string]any{
		"Flag":    flag,
		"Audit":   entries,
		"Page":    "flag",
		"FlagID":  flagID,
		"Feature": strings.Split(flagID, "/")[0],
	}

	if r.Header.Get("HX-Request") == "true" {
		h.render(w, "flag_content", data)
		return
	}
	h.render(w, "layout", data)
}

func (h *Handler) auditLog(w http.ResponseWriter, r *http.Request) {
	flagFilter := r.URL.Query().Get("flag_id")
	actionFilter := r.URL.Query().Get("action")
	actorFilter := r.URL.Query().Get("actor")
	limitStr := r.URL.Query().Get("limit")
	limit := int32(100)
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil {
			limit = int32(v)
		}
	}

	entries, err := h.store.GetAuditLog(r.Context(), admin.AuditLogFilter{
		FlagID: flagFilter,
		Action: actionFilter,
		Actor:  actorFilter,
		Limit:  limit,
	})
	if err != nil {
		h.serverError(w, "get audit log", err)
		return
	}

	data := map[string]any{
		"Entries":      entries,
		"Page":         "audit",
		"FlagFilter":   flagFilter,
		"ActionFilter": actionFilter,
		"ActorFilter":  actorFilter,
	}

	if r.Header.Get("HX-Request") == "true" {
		h.render(w, "audit_content", data)
		return
	}
	h.render(w, "layout", data)
}

// ---------------------------------------------------------------------------
// htmx API handlers
// ---------------------------------------------------------------------------

func (h *Handler) updateFlagState(w http.ResponseWriter, r *http.Request) {
	flagID := r.PathValue("flagID")
	if !validFlagID.MatchString(flagID) {
		http.Error(w, "invalid flag_id format", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	stateStr := r.FormValue("state")
	state := parseStateString(stateStr)
	if state == pbflagsv1.State_STATE_UNSPECIFIED {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	var value *pbflagsv1.FlagValue
	if valueStr := r.FormValue("value"); valueStr != "" {
		flagTypeStr := r.FormValue("flag_type")
		var err error
		value, err = parseFlagValue(flagTypeStr, valueStr)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid value: %v", err), http.StatusBadRequest)
			return
		}
	}

	actor := r.FormValue("actor")
	if actor == "" {
		actor = "admin-ui"
	}

	if err := h.store.UpdateFlagState(r.Context(), flagID, state, value, actor); err != nil {
		h.serverError(w, "update flag state", err)
		return
	}

	// Re-fetch the flag after update.
	flag, err := h.store.GetFlag(r.Context(), flagID)
	if err != nil {
		h.serverError(w, "get flag after update", err)
		return
	}

	// If targeting #content (flag detail page), render full detail view.
	if r.Header.Get("HX-Target") == "content" {
		entries, err := h.store.GetAuditLog(r.Context(), admin.AuditLogFilter{FlagID: flagID, Limit: 20})
		if err != nil {
			h.logger.Error("get audit log after state update", "flag_id", flagID, "error", err)
		}
		h.render(w, "flag_content", map[string]any{
			"Flag":    flag,
			"Audit":   entries,
			"Page":    "flag",
			"FlagID":  flagID,
			"Feature": strings.Split(flagID, "/")[0],
		})
		return
	}

	// Otherwise render just the table row (dashboard view).
	h.render(w, "flag_row", map[string]any{"Flag": flag})
}

func (h *Handler) setOverride(w http.ResponseWriter, r *http.Request) {
	flagID := r.PathValue("flagID")
	if !validFlagID.MatchString(flagID) {
		http.Error(w, "invalid flag_id format", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	entityID := r.FormValue("entity_id")
	if entityID == "" {
		http.Error(w, "entity_id required", http.StatusBadRequest)
		return
	}

	stateStr := r.FormValue("state")
	state := parseStateString(stateStr)
	if state == pbflagsv1.State_STATE_UNSPECIFIED {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	var value *pbflagsv1.FlagValue
	if valueStr := r.FormValue("value"); valueStr != "" {
		flagTypeStr := r.FormValue("flag_type")
		var err error
		value, err = parseFlagValue(flagTypeStr, valueStr)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid value: %v", err), http.StatusBadRequest)
			return
		}
	}

	actor := r.FormValue("actor")
	if actor == "" {
		actor = "admin-ui"
	}

	if err := h.store.SetFlagOverride(r.Context(), flagID, entityID, state, value, actor); err != nil {
		h.serverError(w, "set override", err)
		return
	}

	// Re-render the full overrides section.
	flag, err := h.store.GetFlag(r.Context(), flagID)
	if err != nil {
		h.serverError(w, "get flag after override", err)
		return
	}

	h.render(w, "overrides_section", map[string]any{"Flag": flag})
}

func (h *Handler) removeOverride(w http.ResponseWriter, r *http.Request) {
	flagID := r.PathValue("flagID")
	if !validFlagID.MatchString(flagID) {
		http.Error(w, "invalid flag_id format", http.StatusBadRequest)
		return
	}
	entityID := r.PathValue("entityID")
	if !validEntityPathSegment(entityID) {
		http.Error(w, "invalid entity_id", http.StatusBadRequest)
		return
	}

	actor := "admin-ui"
	if err := h.store.RemoveFlagOverride(r.Context(), flagID, entityID, actor); err != nil {
		h.serverError(w, "remove override", err)
		return
	}

	// Re-render overrides section.
	flag, err := h.store.GetFlag(r.Context(), flagID)
	if err != nil {
		h.serverError(w, "get flag after remove", err)
		return
	}

	h.render(w, "overrides_section", map[string]any{"Flag": flag})
}

// ---------------------------------------------------------------------------
// Template helpers
// ---------------------------------------------------------------------------

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error("render template", "template", name, "error", err)
	}
}

func (h *Handler) serverError(w http.ResponseWriter, ctx string, err error) {
	h.logger.Error(ctx, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Template functions
// ---------------------------------------------------------------------------

func formatFlagValue(v *pbflagsv1.FlagValue) string {
	if v == nil {
		return "—"
	}
	switch val := v.Value.(type) {
	case *pbflagsv1.FlagValue_BoolValue:
		if val.BoolValue {
			return "true"
		}
		return "false"
	case *pbflagsv1.FlagValue_StringValue:
		return fmt.Sprintf("%q", val.StringValue)
	case *pbflagsv1.FlagValue_Int64Value:
		return strconv.FormatInt(val.Int64Value, 10)
	case *pbflagsv1.FlagValue_DoubleValue:
		return strconv.FormatFloat(val.DoubleValue, 'f', -1, 64)
	default:
		return "—"
	}
}

func stateClass(s pbflagsv1.State) string {
	switch s {
	case pbflagsv1.State_STATE_ENABLED:
		return "state-enabled"
	case pbflagsv1.State_STATE_DEFAULT:
		return "state-default"
	case pbflagsv1.State_STATE_KILLED:
		return "state-killed"
	default:
		return "state-unknown"
	}
}

func stateLabel(s pbflagsv1.State) string {
	switch s {
	case pbflagsv1.State_STATE_ENABLED:
		return "ENABLED"
	case pbflagsv1.State_STATE_DEFAULT:
		return "DEFAULT"
	case pbflagsv1.State_STATE_KILLED:
		return "KILLED"
	default:
		return "UNKNOWN"
	}
}

func layerLabel(l pbflagspb.Layer) string {
	switch l {
	case pbflagspb.Layer_LAYER_GLOBAL:
		return "GLOBAL"
	case pbflagspb.Layer_LAYER_USER:
		return "USER"
	default:
		return "GLOBAL"
	}
}

func typeLabel(t pbflagsv1.FlagType) string {
	switch t {
	case pbflagsv1.FlagType_FLAG_TYPE_BOOL:
		return "bool"
	case pbflagsv1.FlagType_FLAG_TYPE_STRING:
		return "string"
	case pbflagsv1.FlagType_FLAG_TYPE_INT64:
		return "int64"
	case pbflagsv1.FlagType_FLAG_TYPE_DOUBLE:
		return "double"
	default:
		return "unknown"
	}
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

func isUserLayer(l pbflagspb.Layer) bool {
	return l == pbflagspb.Layer_LAYER_USER
}

func toJSON(v any) template.JS {
	b, _ := json.Marshal(v)
	return template.JS(b)
}

func dict(pairs ...any) map[string]any {
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs)-1; i += 2 {
		key, _ := pairs[i].(string)
		m[key] = pairs[i+1]
	}
	return m
}

func flagIDEscape(id string) string {
	return strings.ReplaceAll(id, "/", "-")
}

func countFlags(features []*pbflagsv1.FeatureDetail) int {
	n := 0
	for _, f := range features {
		n += len(f.Flags)
	}
	return n
}

// ---------------------------------------------------------------------------
// Input validation
// ---------------------------------------------------------------------------

// validFlagID matches feature_id/field_number, e.g. "notifications/1".
var validFlagID = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(/[0-9]+)+$`)

// validEntityPathSegment rejects values that would break single URL path segments
// (used in DELETE .../entity/{entityID}/{flagID...}).
func validEntityPathSegment(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch r {
		case '/', '?', '#', '\\':
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Value parsing for form submissions
// ---------------------------------------------------------------------------

func parseStateString(s string) pbflagsv1.State {
	switch strings.ToUpper(s) {
	case "ENABLED":
		return pbflagsv1.State_STATE_ENABLED
	case "DEFAULT":
		return pbflagsv1.State_STATE_DEFAULT
	case "KILLED":
		return pbflagsv1.State_STATE_KILLED
	default:
		return pbflagsv1.State_STATE_UNSPECIFIED
	}
}

func parseFlagValue(flagType, raw string) (*pbflagsv1.FlagValue, error) {
	switch strings.ToUpper(flagType) {
	case "BOOL", "FLAG_TYPE_BOOL", "1":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("parse bool: %w", err)
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: b}}, nil
	case "STRING", "FLAG_TYPE_STRING", "2":
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: raw}}, nil
	case "INT64", "FLAG_TYPE_INT64", "3":
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse int64: %w", err)
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: v}}, nil
	case "DOUBLE", "FLAG_TYPE_DOUBLE", "4":
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("parse double: %w", err)
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: v}}, nil
	default:
		return nil, fmt.Errorf("unknown flag type %q", flagType)
	}
}
