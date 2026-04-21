package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// Two independent workers run for the lifetime of the process:
//
//   runSilenceAlerter — every few minutes, checks each key's last_seen_at.
//                       If a previously-active key has been silent for
//                       silence_alert_hours (default 1h) we send an email
//                       to the owner and record the alert so we don't
//                       send it again until the key comes back to life.
//
//   runReportWorker   — every hour, checks each user's cadence and sends
//                       a play report covering the last period. Hourly
//                       polling is good enough: daily/weekly/monthly fire
//                       on the expected day, and calendar_month fires on
//                       the 1st of each month. The worker persists nothing
//                       — if the process misses a hour we tolerate one
//                       skipped report rather than building a scheduler.

const (
	silenceCheckInterval = 5 * time.Minute
	reportCheckInterval  = 1 * time.Hour
)

// ---- silence alerter ----

func runSilenceAlerter(ctx context.Context, app *App) {
	// Small initial delay so startup smoke tests don't get paged.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}

	t := time.NewTicker(silenceCheckInterval)
	defer t.Stop()
	for {
		if err := silenceAlertPass(ctx, app); err != nil {
			log.Printf("[silence] pass failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func silenceAlertPass(ctx context.Context, app *App) error {
	threshold := time.Duration(app.res.Cfg.SilenceAlertHours) * time.Hour
	if threshold <= 0 {
		threshold = time.Hour
	}
	cands, err := app.store.KeyAlertCandidates(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, c := range cands {
		// Never-seen keys: nothing to alert about. Fresh keys with zero
		// plays will only start generating alerts after they go quiet.
		if !c.LastSeenAt.Valid {
			continue
		}
		lastSeen := c.LastSeenAt.Timestamp
		age := now.Sub(lastSeen)
		if age < threshold {
			continue
		}
		// Dedup: only alert if we haven't already alerted for this
		// silence period. A fresh play resets last_seen_at so it moves
		// past the threshold again before the next alert.
		if c.LastAlertedAt.Valid && !lastSeen.After(c.LastAlertedAt.Timestamp) {
			continue
		}
		email := ""
		if c.Email.Valid {
			email = c.Email.StringVal
		}
		if email == "" {
			continue
		}
		label := ""
		if c.Label.Valid {
			label = c.Label.StringVal
		}
		subject := fmt.Sprintf("Radio Playlog: no data from %q", labelOrHash(label, c.KeyHash))
		body := buildSilenceEmailBody(c.KeyHash, label, lastSeen, age)
		if err := app.mailer.Send([]string{email}, subject, body); err != nil {
			log.Printf("[silence] send failed for user=%s: %v", c.UserID, err)
			continue
		}
		if err := app.store.RecordAlert(ctx, c.UserID, c.KeyHash, lastSeen, now); err != nil {
			log.Printf("[silence] RecordAlert failed user=%s: %v", c.UserID, err)
		}
	}
	return nil
}

func buildSilenceEmailBody(keyHash, label string, lastSeen time.Time, age time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Heads up — your Radio Playlog key %q (%s…) has not received any data for %s.\n\n",
		labelOrHash(label, keyHash), safePrefix(keyHash, 12), formatAge(age))
	fmt.Fprintf(&b, "Last play was at %s (UTC).\n\n",
		lastSeen.UTC().Format(time.RFC3339))
	b.WriteString("If this was planned (studio off air, maintenance, etc.) you can ignore this. Otherwise check that your logger still points at the right URL and that the API key is still enabled.\n")
	return b.String()
}

func labelOrHash(label, hash string) string {
	if label != "" {
		return label
	}
	return safePrefix(hash, 12) + "…"
}

func safePrefix(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

func formatAge(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%.1f hours", d.Hours())
	}
	return fmt.Sprintf("%.1f days", d.Hours()/24)
}

// ---- report worker ----

func runReportWorker(ctx context.Context, app *App) {
	// Offset the first run so logs are readable and we don't collide
	// with startup migrations.
	select {
	case <-ctx.Done():
		return
	case <-time.After(90 * time.Second):
	}
	t := time.NewTicker(reportCheckInterval)
	defer t.Stop()
	for {
		if err := reportPass(ctx, app); err != nil {
			log.Printf("[reports] pass failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// reportPass runs hourly. For each configured user it asks: should a
// report fire right now? The "right now" test is deliberately crude —
// we fire once per tick when the user's local clock is inside the first
// hour of the target day. The worker doesn't remember what it sent
// before, so it relies on the firing window being short.
func reportPass(ctx context.Context, app *App) error {
	if !app.mailer.Enabled() {
		// No SMTP configured → no reports. The alerter also short-
		// circuits through the logMailer, but a full report with an
		// attachment is noisier to no-op over, so skip entirely.
		return nil
	}
	all, err := app.store.ListAllSettings(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, st := range all {
		if st.Cadence == "" || st.Cadence == "off" {
			continue
		}
		emails := []string{}
		if st.ReportEmails != "" {
			_ = json.Unmarshal([]byte(st.ReportEmails), &emails)
		}
		if len(emails) == 0 {
			continue
		}
		loc := resolveLocation(st.Timezone)
		local := now.In(loc)

		fire, from, to := shouldFire(st.Cadence, local, loc)
		if !fire {
			continue
		}
		if err := sendReport(ctx, app, st.UserID, emails, loc, from, to, st.Cadence); err != nil {
			log.Printf("[reports] send failed user=%s cadence=%s: %v", st.UserID, st.Cadence, err)
		}
	}
	return nil
}

// shouldFire returns whether a report should go out right now for a
// given cadence, and the [from, to) window it should cover. The window
// is always in the user's local timezone. We fire on the first hour of
// the day the report is due for:
//
//	daily          — every day at 00:xx local, covers the previous day.
//	weekly         — Monday at 00:xx local, covers Mon..Sun of last week.
//	monthly        — 1st at 00:xx local, covers the last 30 days.
//	calendar_month — 1st at 00:xx local, covers the previous calendar month.
func shouldFire(cadence string, local time.Time, loc *time.Location) (bool, time.Time, time.Time) {
	if local.Hour() != 0 {
		return false, time.Time{}, time.Time{}
	}
	switch cadence {
	case "daily":
		end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		start := end.AddDate(0, 0, -1)
		return true, start, end
	case "weekly":
		if local.Weekday() != time.Monday {
			return false, time.Time{}, time.Time{}
		}
		end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		start := end.AddDate(0, 0, -7)
		return true, start, end
	case "monthly":
		if local.Day() != 1 {
			return false, time.Time{}, time.Time{}
		}
		end := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		start := end.AddDate(0, 0, -30)
		return true, start, end
	case "calendar_month":
		if local.Day() != 1 {
			return false, time.Time{}, time.Time{}
		}
		from, to := previousCalendarMonth(local, loc)
		return true, from, to
	}
	return false, time.Time{}, time.Time{}
}

func sendReport(ctx context.Context, app *App, userID string, emails []string, loc *time.Location, from, to time.Time, cadence string) error {
	plays, err := app.store.QueryPlays(ctx, userID, "", from, to, 500000)
	if err != nil {
		return err
	}
	cols := []string{"timestamp", "artist", "title", "studio_name"}
	rows := make([]map[string]any, 0, len(plays))
	for _, p := range plays {
		rows = append(rows, map[string]any{
			"timestamp":   p.Timestamp.In(loc).Format(time.RFC3339),
			"artist":      nullToString(p.Artist),
			"title":       nullToString(p.Title),
			"studio_name": nullToString(p.Label),
		})
	}
	csvBytes, err := formatExport("csv", cols, rows)
	if err != nil {
		return err
	}
	tag := from.In(loc).Format("20060102") + "-" + to.In(loc).Format("20060102")
	att := Attachment{
		Filename:    "playlog-" + tag + ".csv",
		ContentType: "text/csv",
		Data:        csvBytes,
	}
	subject := fmt.Sprintf("Radio Playlog report — %s (%d plays)", tag, len(rows))
	body := fmt.Sprintf("Attached: your %s Radio Playlog report covering %s to %s (%s).\n\n%d plays.\n",
		cadence,
		from.In(loc).Format("2006-01-02 15:04"),
		to.In(loc).Format("2006-01-02 15:04"),
		loc.String(),
		len(rows))
	return app.mailer.SendWithAttachment(emails, subject, body, att)
}
