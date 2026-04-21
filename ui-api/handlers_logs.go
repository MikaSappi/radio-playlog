package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---- /api/logs — logs explorer ----
//
// The logs explorer lets the signed-in user confirm, at a glance, that
// their loggers are actually sending plays. This is read-only and scoped
// to the caller's user_id — no cross-user visibility.

const (
	logsDefaultLimit = 100
	logsMaxLimit     = 1000
)

func (app *App) handleLogsGet(w http.ResponseWriter, r *http.Request, userID string) {
	st, err := app.store.GetSettings(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "settings lookup failed"})
		return
	}
	loc := resolveLocation(st.Timezone)

	q := r.URL.Query()

	// Time window. Defaults to "last 24h" when the client sends nothing.
	now := time.Now()
	to := now
	from := now.Add(-24 * time.Hour)
	if s := q.Get("from"); s != "" {
		t, err := parseRFC3339OrDate(s, loc)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		from = t
	}
	if s := q.Get("to"); s != "" {
		t, err := parseRFC3339OrDate(s, loc)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		to = t
	}
	if !to.After(from) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "'to' must be after 'from'"})
		return
	}

	limit := logsDefaultLimit
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad limit"})
			return
		}
		if n > logsMaxLimit {
			n = logsMaxLimit
		}
		limit = n
	}

	keyHash := strings.ToLower(strings.TrimSpace(q.Get("key_hash")))
	if keyHash != "" && !keyHashRe.MatchString(keyHash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad key_hash"})
		return
	}

	plays, err := app.store.QueryPlays(r.Context(), userID, keyHash, from, to, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}

	out := make([]map[string]any, 0, len(plays))
	for _, p := range plays {
		out = append(out, map[string]any{
			"timestamp":       p.Timestamp.UTC().Format(time.RFC3339),
			"timestamp_local": p.Timestamp.In(loc).Format(time.RFC3339),
			"title":           nullToString(p.Title),
			"artist":          nullToString(p.Artist),
			"key_hash":        nullToString(p.KeyHash),
			"label":           nullToString(p.Label),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plays":    out,
		"timezone": st.Timezone,
		"from":     from.UTC().Format(time.RFC3339),
		"to":       to.UTC().Format(time.RFC3339),
		"limit":    limit,
		"count":    len(out),
	})
}

// ---- /api/export — export builder ----
//
// The user picks columns (subset of title/artist/timestamp/studio_name),
// a format (csv/json/ndjson/xml/yaml/toml), and a period (date range or
// calendar-month preset). We render synchronously and return the file as
// a download.

type exportRequest struct {
	Format  string   `json:"format"`
	Columns []string `json:"columns"`
	Preset  string   `json:"preset"` // "previous_calendar_month" | "current_calendar_month" | ""
	From    string   `json:"from"`
	To      string   `json:"to"`
	KeyHash string   `json:"key_hash"`
}

var validExportColumns = map[string]bool{
	"title":        true,
	"artist":       true,
	"timestamp":    true,
	"studio_name":  true,
}

var validExportFormats = map[string]string{
	"csv":    "text/csv",
	"json":   "application/json",
	"ndjson": "application/x-ndjson",
	"xml":    "application/xml",
	"yaml":   "application/yaml",
	"toml":   "application/toml",
}

func (app *App) handleExport(w http.ResponseWriter, r *http.Request, userID string) {
	var req exportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}

	format := strings.ToLower(strings.TrimSpace(req.Format))
	mime, ok := validExportFormats[format]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "format must be one of: csv, json, ndjson, xml, yaml, toml",
		})
		return
	}

	// Dedup columns while preserving order. An empty column list means
	// "all four" — a friendly default.
	cols := []string{}
	seen := map[string]bool{}
	for _, c := range req.Columns {
		c = strings.ToLower(strings.TrimSpace(c))
		if !validExportColumns[c] || seen[c] {
			if c != "" && !validExportColumns[c] {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "unknown column: " + c,
				})
				return
			}
			continue
		}
		cols = append(cols, c)
		seen[c] = true
	}
	if len(cols) == 0 {
		cols = []string{"timestamp", "artist", "title", "studio_name"}
	}

	st, err := app.store.GetSettings(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "settings lookup failed"})
		return
	}
	loc := resolveLocation(st.Timezone)

	var from, to time.Time
	switch req.Preset {
	case "previous_calendar_month":
		from, to = previousCalendarMonth(time.Now(), loc)
	case "current_calendar_month":
		from, to = calendarMonthRange(time.Now(), loc)
	case "", "custom":
		from, err = parseRFC3339OrDate(req.From, loc)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from: " + err.Error()})
			return
		}
		to, err = parseRFC3339OrDate(req.To, loc)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to: " + err.Error()})
			return
		}
		// The convention in the UI is inclusive-date: selecting 2026-01-01
		// through 2026-01-31 should cover all of January. If the user sent
		// a bare date for `to`, bump it forward one day so we include the
		// whole 31st.
		if len(req.To) == len("2006-01-02") {
			to = to.AddDate(0, 0, 1)
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown preset"})
		return
	}
	if !to.After(from) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "'to' must be after 'from'"})
		return
	}

	keyHash := strings.ToLower(strings.TrimSpace(req.KeyHash))
	if keyHash != "" && !keyHashRe.MatchString(keyHash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad key_hash"})
		return
	}

	plays, err := app.store.QueryPlays(r.Context(), userID, keyHash, from, to, 500000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}

	// Build a uniform row shape keyed by the requested columns so the
	// formatters don't have to know the domain model. `timestamp` is
	// rendered in the user's timezone; `studio_name` comes from the
	// label snapshot on each row.
	rows := make([]map[string]any, 0, len(plays))
	for _, p := range plays {
		row := map[string]any{}
		for _, c := range cols {
			switch c {
			case "title":
				row[c] = nullToString(p.Title)
			case "artist":
				row[c] = nullToString(p.Artist)
			case "timestamp":
				row[c] = p.Timestamp.In(loc).Format(time.RFC3339)
			case "studio_name":
				row[c] = nullToString(p.Label)
			}
		}
		rows = append(rows, row)
	}

	data, err := formatExport(format, cols, rows)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "format failed: " + err.Error()})
		return
	}

	filename := exportFilename(format, from, to, loc, keyHash)
	w.Header().Set("Content-Type", mime+"; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Row-Count", strconv.Itoa(len(rows)))
	_, _ = w.Write(data)
}

func exportFilename(format string, from, to time.Time, loc *time.Location, keyHash string) string {
	ext := format
	tag := from.In(loc).Format("20060102") + "-" + to.In(loc).Format("20060102")
	base := "playlog-" + tag
	if keyHash != "" {
		base += "-" + keyHash[:8]
	}
	return base + "." + ext
}
