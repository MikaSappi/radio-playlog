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
)

// App bundles the shared dependencies needed by every HTTP handler.
type App struct {
	res       *Resolved
	store     *Store
	providers map[string]*providerCfg
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
	if keys == nil {
		keys = []APIKey{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
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
	k := APIKey{
		KeyHash:   hash,
		UserID:    userID,
		Enabled:   true,
		Label:     label,
		CreatedAt: time.Now().UTC(),
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
		"created_at": k.CreatedAt,
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

// ---- /api/settings ----

type settingsPayload struct {
	Emails  []string `json:"emails"`
	Cadence string   `json:"cadence"`
}

func validCadence(c string) bool {
	switch c {
	case "daily", "weekly", "monthly", "off":
		return true
	}
	return false
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
	writeJSON(w, http.StatusOK, settingsPayload{Emails: emails, Cadence: cadence})
}

func (app *App) handleSettingsPut(w http.ResponseWriter, r *http.Request, userID string) {
	var p settingsPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if !validCadence(p.Cadence) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cadence must be one of: daily, weekly, monthly, off"})
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
	if err := app.store.UpsertSettings(r.Context(), userID, string(emailsJSON), p.Cadence); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
		return
	}
	writeJSON(w, http.StatusOK, settingsPayload{Emails: cleaned, Cadence: p.Cadence})
}

// ---- util ----

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
