package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
)

// App bundles the shared dependencies needed by every HTTP handler.
type App struct {
	res       *Resolved
	store     *Store
	providers map[string]*providerCfg
	mailer    Mailer
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// requireUser gates an API handler behind a valid session cookie.
func (app *App) requireUser(h func(w http.ResponseWriter, r *http.Request, userID string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := userIDFromRequest(r, app.res.SessionSecret)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r, uid)
	}
}

// ---- /api/me ----

func (app *App) handleMe(w http.ResponseWriter, r *http.Request, userID string) {
	u, err := app.store.GetUser(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "user lookup failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":  u.UserID,
		"email":    u.Email,
		"provider": u.Provider,
	})
}

// ---- /api/logout ----

func (app *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- /api/keys ----

func (app *App) handleKeysGet(w http.ResponseWriter, r *http.Request, userID string) {
	keys, err := app.store.ListKeys(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}

	// Shape the response so the UI gets last_seen_at as either an RFC3339
	// string or null — simpler than asking the client to special-case Go's
	// zero-time. Also pre-compute the LED status so every client agrees on
	// what "live" means.
	now := time.Now().UTC()
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		var lastSeen any = nil
		var lastSeenT time.Time
		if k.LastSeenAt.Valid {
			lastSeenT = k.LastSeenAt.Timestamp
			lastSeen = lastSeenT.UTC().Format(time.RFC3339)
		}
		var created any = nil
		if k.CreatedAt.Valid {
			created = k.CreatedAt.Timestamp.UTC().Format(time.RFC3339)
		}
		label := ""
		if k.Label.Valid {
			label = k.Label.StringVal
		}
		out = append(out, map[string]any{
			"key_hash":     k.KeyHash,
			"enabled":      k.Enabled,
			"label":        label,
			"created_at":   created,
			"last_seen_at": lastSeen,
			"status":       ledStatus(lastSeenT, now, k.Enabled),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// ledStatus maps an age into the LED colors the UI expects:
//
//	green : received data in the last 5 min
//	yellow: received data in the last hour but not the last 5 min
//	red   : no data in the last hour (but has received before)
//	idle  : never received data (fresh key)
//	off   : disabled
//
// We compute this server-side so every client agrees, and so the alerter
// shares a definition of "silent" with the UI.
func ledStatus(lastSeen, now time.Time, enabled bool) string {
	if !enabled {
		return "off"
	}
	if lastSeen.IsZero() {
		return "idle"
	}
	age := now.Sub(lastSeen)
	switch {
	case age <= 5*time.Minute:
		return "green"
	case age <= time.Hour:
		return "yellow"
	default:
		return "red"
	}
}

func (app *App) handleKeysPost(w http.ResponseWriter, r *http.Request, userID string) {
	var req struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	label := strings.TrimSpace(req.Label)
	if len(label) > 80 {
		label = label[:80]
	}

	raw, hash, err := newAPIKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "key gen failed"})
		return
	}
	now := time.Now().UTC()
	k := APIKey{
		KeyHash:   hash,
		UserID:    userID,
		Enabled:   true,
		Label:     bigquery.NullString{StringVal: label, Valid: label != ""},
		CreatedAt: bigquery.NullTimestamp{Timestamp: now, Valid: true},
	}
	if err := app.store.InsertKey(r.Context(), k); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "insert failed"})
		return
	}
	// Raw key is returned exactly once. There is no way to retrieve it later.
	writeJSON(w, http.StatusOK, map[string]any{
		"key":        raw,
		"key_hash":   hash,
		"label":      label,
		"created_at": now,
	})
}

var keyHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (app *App) handleKeyDisable(w http.ResponseWriter, r *http.Request, userID string) {
	hash := strings.TrimPrefix(r.URL.Path, "/api/keys/")
	hash = strings.ToLower(hash)
	if !keyHashRe.MatchString(hash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad key_hash"})
		return
	}
	if err := app.store.DisableKey(r.Context(), userID, hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "disable failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleKeyRename updates the label on a single key. The logger snapshots
// the label into each play row at write time, so this rename only affects
// future entries — exactly what the user asked for.
func (app *App) handleKeyRename(w http.ResponseWriter, r *http.Request, userID string) {
	hash := strings.TrimPrefix(r.URL.Path, "/api/keys/")
	hash = strings.ToLower(hash)
	if !keyHashRe.MatchString(hash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad key_hash"})
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "label required"})
		return
	}
	if len(label) > 80 {
		label = label[:80]
	}
	if err := app.store.RenameKey(r.Context(), userID, hash, label); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "rename failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "label": label})
}

// ---- /api/settings ----

type settingsPayload struct {
	Emails   []string `json:"emails"`
	Cadence  string   `json:"cadence"`
	Timezone string   `json:"timezone"`
}

func validCadence(c string) bool {
	switch c {
	case "daily", "weekly", "monthly", "calendar_month", "off":
		return true
	}
	return false
}

// validTimezone accepts the empty string (meaning "unset — treat as UTC")
// and any IANA zone recognized by the Go runtime. The user-supplied name
// is stored verbatim, so we round-trip it through time.LoadLocation to
// validate rather than trusting the client.
func validTimezone(tz string) bool {
	if tz == "" {
		return true
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

func (app *App) handleSettingsGet(w http.ResponseWriter, r *http.Request, userID string) {
	st, err := app.store.GetSettings(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "settings read failed"})
		return
	}
	emails := []string{}
	if st.ReportEmails != "" {
		_ = json.Unmarshal([]byte(st.ReportEmails), &emails)
	}
	cadence := st.Cadence
	if cadence == "" {
		cadence = "off"
	}
	writeJSON(w, http.StatusOK, settingsPayload{
		Emails:   emails,
		Cadence:  cadence,
		Timezone: st.Timezone,
	})
}

func (app *App) handleSettingsPut(w http.ResponseWriter, r *http.Request, userID string) {
	var p settingsPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if !validCadence(p.Cadence) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cadence must be one of: daily, weekly, monthly, calendar_month, off"})
		return
	}
	if !validTimezone(p.Timezone) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown timezone (use an IANA name, e.g. Europe/Helsinki)"})
		return
	}
	cleaned := make([]string, 0, len(p.Emails))
	for _, e := range p.Emails {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, err := mail.ParseAddress(e); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid email: " + e})
			return
		}
		cleaned = append(cleaned, e)
	}
	if len(cleaned) > 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too many emails (max 10)"})
		return
	}
	emailsJSON, _ := json.Marshal(cleaned)
	if err := app.store.UpsertSettings(r.Context(), userID, string(emailsJSON), p.Cadence, p.Timezone); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
		return
	}
	writeJSON(w, http.StatusOK, settingsPayload{Emails: cleaned, Cadence: p.Cadence, Timezone: p.Timezone})
}

// ---- util ----

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
