package main

import (
	"fmt"
	"time"
)

// resolveLocation picks the time.Location for a user. An empty name or an
// unrecognized one falls back to UTC — the same thing we'd do on the write
// side if the user hadn't configured one.
func resolveLocation(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// calendarMonthRange returns [first-of-month, first-of-next-month) for the
// month containing `ref`, in the given location. Used by the calendar_month
// cadence and by the export builder's "previous calendar month" preset.
func calendarMonthRange(ref time.Time, loc *time.Location) (from, to time.Time) {
	t := ref.In(loc)
	from = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	to = from.AddDate(0, 1, 0)
	return
}

// previousCalendarMonth is the calendar month strictly before `ref`.
func previousCalendarMonth(ref time.Time, loc *time.Location) (from, to time.Time) {
	thisFrom, _ := calendarMonthRange(ref, loc)
	from = thisFrom.AddDate(0, -1, 0)
	to = thisFrom
	return
}

// parseRFC3339OrDate accepts either a full RFC3339 timestamp or a
// YYYY-MM-DD date. Dates are interpreted in the user's timezone — the UI
// sends bare dates for date pickers and we treat them as "midnight local".
func parseRFC3339OrDate(s string, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q (use RFC3339 or YYYY-MM-DD)", s)
}
