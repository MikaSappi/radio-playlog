package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
)

// Store wraps the BigQuery client and the table names the UI backend reads
// and writes. We also read the plays table here (read-only — writes go
// through the logger), for per-key status, exports, and the logs explorer.
type Store struct {
	client       *bigquery.Client
	project      string
	dataset      string
	tableUsers   string
	tableAPIKeys string
	tableSet     string
	tablePlays   string
	tableAlerts  string
}

func NewStore(ctx context.Context, r *Resolved) (*Store, error) {
	client, err := bigquery.NewClient(ctx, r.Cfg.GCPProject)
	if err != nil {
		return nil, err
	}
	return &Store{
		client:       client,
		project:      r.Cfg.GCPProject,
		dataset:      r.Cfg.BQDataset,
		tableUsers:   r.Cfg.BQTableUsers,
		tableAPIKeys: r.Cfg.BQTableAPIKeys,
		tableSet:     r.Cfg.BQTableSettings,
		tablePlays:   r.Cfg.BQTablePlays,
		tableAlerts:  r.Cfg.BQTableAlerts,
	}, nil
}

func (s *Store) Close() { _ = s.client.Close() }

func (s *Store) fq(table string) string {
	return fmt.Sprintf("`%s.%s.%s`", s.project, s.dataset, table)
}

// ---- users ----

type User struct {
	UserID      string    `bigquery:"user_id"`
	Email       string    `bigquery:"email"`
	Provider    string    `bigquery:"provider"`
	ProviderSub string    `bigquery:"provider_sub"`
	CreatedAt   time.Time `bigquery:"created_at"`
}

// FindOrCreateUser looks up a user by (provider, provider_sub). Creates one
// with a generated short user_id if missing. Returns the user row.
func (s *Store) FindOrCreateUser(ctx context.Context, provider, providerSub, email string) (*User, error) {
	q := s.client.Query(fmt.Sprintf(
		"SELECT user_id, email, provider, provider_sub, created_at FROM %s WHERE provider = @p AND provider_sub = @sub LIMIT 1",
		s.fq(s.tableUsers),
	))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "p", Value: provider},
		{Name: "sub", Value: providerSub},
	}
	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	var existing User
	err = it.Next(&existing)
	if err == nil {
		return &existing, nil
	}
	if err != iterator.Done {
		return nil, err
	}

	uid, err := newUserID()
	if err != nil {
		return nil, err
	}
	u := User{
		UserID:      uid,
		Email:       email,
		Provider:    provider,
		ProviderSub: providerSub,
		CreatedAt:   time.Now().UTC(),
	}
	inserter := s.client.Dataset(s.dataset).Table(s.tableUsers).Inserter()
	if err := inserter.Put(ctx, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) GetUser(ctx context.Context, userID string) (*User, error) {
	q := s.client.Query(fmt.Sprintf(
		"SELECT user_id, email, provider, provider_sub, created_at FROM %s WHERE user_id = @uid LIMIT 1",
		s.fq(s.tableUsers),
	))
	q.Parameters = []bigquery.QueryParameter{{Name: "uid", Value: userID}}
	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	var u User
	if err := it.Next(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

// ---- api keys ----

type APIKey struct {
	KeyHash string              `bigquery:"key_hash"   json:"key_hash"`
	UserID  string              `bigquery:"user_id"    json:"-"`
	Enabled bool                `bigquery:"enabled"    json:"enabled"`
	Label   bigquery.NullString `bigquery:"label"      json:"-"`
	// CreatedAt is nullable because it was added as a nullable column
	// after the table already held rows; legacy rows have NULL.
	CreatedAt bigquery.NullTimestamp `bigquery:"created_at" json:"-"`
	// LastSeenAt is nullable: a brand-new key with no plays yet produces
	// a NULL from the LEFT JOIN in ListKeys. Using NullTimestamp avoids
	// the Go BQ client balking on NULL→time.Time.
	LastSeenAt bigquery.NullTimestamp `bigquery:"last_seen_at" json:"-"`
}

// ListKeys returns all the user's keys, LEFT JOINed with the most recent
// play timestamp seen for that key. last_seen_at is zero when the key has
// never received a play. The UI uses this for the status LED.
func (s *Store) ListKeys(ctx context.Context, userID string) ([]APIKey, error) {
	q := s.client.Query(fmt.Sprintf(`
SELECT k.key_hash, k.user_id, k.enabled, k.label, k.created_at,
       p.last_seen_at
FROM %s k
LEFT JOIN (
  SELECT key_hash, MAX(timestamp) AS last_seen_at
  FROM %s
  WHERE user_id = @uid AND key_hash IS NOT NULL
  GROUP BY key_hash
) p
ON LOWER(k.key_hash) = LOWER(p.key_hash)
WHERE k.user_id = @uid
ORDER BY k.created_at DESC
`, s.fq(s.tableAPIKeys), s.fq(s.tablePlays)))
	q.Parameters = []bigquery.QueryParameter{{Name: "uid", Value: userID}}
	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	var out []APIKey
	for {
		var k APIKey
		err := it.Next(&k)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

func (s *Store) InsertKey(ctx context.Context, k APIKey) error {
	inserter := s.client.Dataset(s.dataset).Table(s.tableAPIKeys).Inserter()
	return inserter.Put(ctx, &k)
}

// DisableKey flips enabled=FALSE. We never actually delete rows — streaming
// inserts and DELETE don't mix well, and disabled keys stay readable for
// audit.
func (s *Store) DisableKey(ctx context.Context, userID, keyHash string) error {
	q := s.client.Query(fmt.Sprintf(
		"UPDATE %s SET enabled = FALSE WHERE user_id = @uid AND key_hash = @kh",
		s.fq(s.tableAPIKeys),
	))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "uid", Value: userID},
		{Name: "kh", Value: strings.ToLower(keyHash)},
	}
	job, err := q.Run(ctx)
	if err != nil {
		return err
	}
	status, err := job.Wait(ctx)
	if err != nil {
		return err
	}
	return status.Err()
}

// RenameKey updates the label on the api_keys row. Past plays are NOT
// rewritten — each play row carries its label snapshot from the logger.
// This is deliberate per the user's requirement: history stays as it was.
func (s *Store) RenameKey(ctx context.Context, userID, keyHash, label string) error {
	q := s.client.Query(fmt.Sprintf(
		"UPDATE %s SET label = @lbl WHERE user_id = @uid AND key_hash = @kh",
		s.fq(s.tableAPIKeys),
	))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "uid", Value: userID},
		{Name: "kh", Value: strings.ToLower(keyHash)},
		{Name: "lbl", Value: label},
	}
	job, err := q.Run(ctx)
	if err != nil {
		return err
	}
	status, err := job.Wait(ctx)
	if err != nil {
		return err
	}
	return status.Err()
}

// ---- key alerts (silence detection) ----

// KeyAlertState is what the silence worker needs: current last_seen_at and
// the last time we emailed the user about this key. NULL timestamps arrive
// as zero-value time.Time.
type KeyAlertState struct {
	KeyHash string `bigquery:"key_hash"`
	UserID  string `bigquery:"user_id"`
	// Email / Label are nullable in their source tables so we use
	// NullString here and unwrap in the worker.
	Email         bigquery.NullString    `bigquery:"email"`
	Label         bigquery.NullString    `bigquery:"label"`
	LastSeenAt    bigquery.NullTimestamp `bigquery:"last_seen_at"`
	LastAlertedAt bigquery.NullTimestamp `bigquery:"last_alerted_at"`
}

// KeyAlertCandidates returns, for every enabled key that has ever seen a
// play, its current last_seen_at and last_alerted_at along with the owner's
// email. This is the raw input the silence alerter consumes.
func (s *Store) KeyAlertCandidates(ctx context.Context) ([]KeyAlertState, error) {
	q := s.client.Query(fmt.Sprintf(`
SELECT
  k.key_hash       AS key_hash,
  k.user_id        AS user_id,
  u.email          AS email,
  k.label          AS label,
  p.last_seen_at   AS last_seen_at,
  a.last_alerted_at AS last_alerted_at
FROM %s k
JOIN %s u ON u.user_id = k.user_id
LEFT JOIN (
  SELECT key_hash, MAX(timestamp) AS last_seen_at
  FROM %s
  WHERE key_hash IS NOT NULL
  GROUP BY key_hash
) p ON LOWER(p.key_hash) = LOWER(k.key_hash)
LEFT JOIN %s a ON LOWER(a.key_hash) = LOWER(k.key_hash)
WHERE k.enabled = TRUE
`, s.fq(s.tableAPIKeys), s.fq(s.tableUsers), s.fq(s.tablePlays), s.fq(s.tableAlerts)))
	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	var out []KeyAlertState
	for {
		var r KeyAlertState
		err := it.Next(&r)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// RecordAlert stamps last_alerted_at + last_seen_at onto key_alerts, so we
// don't re-send alerts until the key goes silent again after resuming.
func (s *Store) RecordAlert(ctx context.Context, userID, keyHash string, lastSeen, alertedAt time.Time) error {
	q := s.client.Query(fmt.Sprintf(`
MERGE %s T
USING (
  SELECT @kh AS key_hash, @uid AS user_id,
         @lastseen AS last_seen_at, @alertedat AS last_alerted_at,
         CURRENT_TIMESTAMP() AS updated_at
) S
ON LOWER(T.key_hash) = LOWER(S.key_hash)
WHEN MATCHED THEN UPDATE SET
  last_seen_at = S.last_seen_at,
  last_alerted_at = S.last_alerted_at,
  updated_at = S.updated_at
WHEN NOT MATCHED THEN INSERT (key_hash, user_id, last_seen_at, last_alerted_at, updated_at)
VALUES (S.key_hash, S.user_id, S.last_seen_at, S.last_alerted_at, S.updated_at)
`, s.fq(s.tableAlerts)))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "kh", Value: strings.ToLower(keyHash)},
		{Name: "uid", Value: userID},
		{Name: "lastseen", Value: lastSeen},
		{Name: "alertedat", Value: alertedAt},
	}
	job, err := q.Run(ctx)
	if err != nil {
		return err
	}
	status, err := job.Wait(ctx)
	if err != nil {
		return err
	}
	return status.Err()
}

// ---- user settings ----

type Settings struct {
	UserID       string    `bigquery:"user_id"       json:"-"`
	ReportEmails string    `bigquery:"report_emails" json:"-"`
	Cadence      string    `bigquery:"cadence"       json:"cadence"`
	Timezone     string    `bigquery:"timezone"      json:"timezone"`
	UpdatedAt    time.Time `bigquery:"updated_at"    json:"updated_at"`
}

// GetSettings returns the current settings for a user, or a zero-valued row
// if none exist yet. Callers treat empty Emails / Cadence / Timezone as
// unset.
func (s *Store) GetSettings(ctx context.Context, userID string) (*Settings, error) {
	q := s.client.Query(fmt.Sprintf(
		"SELECT user_id, report_emails, cadence, timezone, updated_at FROM %s WHERE user_id = @uid LIMIT 1",
		s.fq(s.tableSet),
	))
	q.Parameters = []bigquery.QueryParameter{{Name: "uid", Value: userID}}
	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	var st Settings
	err = it.Next(&st)
	if err == iterator.Done {
		return &Settings{UserID: userID}, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// UpsertSettings writes settings via MERGE so the row is created on first
// save and updated on subsequent saves.
func (s *Store) UpsertSettings(ctx context.Context, userID, emailsJSON, cadence, timezone string) error {
	q := s.client.Query(fmt.Sprintf(`
MERGE %s T
USING (SELECT @uid AS user_id, @emails AS report_emails, @cadence AS cadence, @tz AS timezone, CURRENT_TIMESTAMP() AS updated_at) S
ON T.user_id = S.user_id
WHEN MATCHED THEN UPDATE SET report_emails = S.report_emails, cadence = S.cadence, timezone = S.timezone, updated_at = S.updated_at
WHEN NOT MATCHED THEN INSERT (user_id, report_emails, cadence, timezone, updated_at)
VALUES (S.user_id, S.report_emails, S.cadence, S.timezone, S.updated_at)
`, s.fq(s.tableSet)))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "uid", Value: userID},
		{Name: "emails", Value: emailsJSON},
		{Name: "cadence", Value: cadence},
		{Name: "tz", Value: timezone},
	}
	job, err := q.Run(ctx)
	if err != nil {
		return err
	}
	status, err := job.Wait(ctx)
	if err != nil {
		return err
	}
	return status.Err()
}

// ListAllSettings is used by the report worker to enumerate every user
// who might need a scheduled report. Users without a row are skipped.
func (s *Store) ListAllSettings(ctx context.Context) ([]Settings, error) {
	q := s.client.Query(fmt.Sprintf(
		"SELECT user_id, report_emails, cadence, timezone, updated_at FROM %s",
		s.fq(s.tableSet),
	))
	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	var out []Settings
	for {
		var st Settings
		err := it.Next(&st)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// ---- plays queries (read-only) ----

// Play is a single played track row. When a key_hash is missing (legacy
// rows from before the logger added it) we surface empty strings rather
// than exposing sql.NullString to handlers.
type Play struct {
	Timestamp time.Time           `bigquery:"timestamp"`
	Title     bigquery.NullString `bigquery:"title"`
	Artist    bigquery.NullString `bigquery:"artist"`
	KeyHash   bigquery.NullString `bigquery:"key_hash"`
	Label     bigquery.NullString `bigquery:"label"`
}

// QueryPlays fetches a user's plays over a time range, newest first, with
// an optional key_hash filter. Used by both the export builder and the
// logs explorer. The hard row cap keeps a badly-picked range from blowing
// up memory — callers should respect it and narrow the window.
func (s *Store) QueryPlays(ctx context.Context, userID, keyHash string, from, to time.Time, limit int) ([]Play, error) {
	if limit <= 0 || limit > 500000 {
		limit = 500000
	}
	where := "user_id = @uid AND timestamp >= @from AND timestamp < @to"
	params := []bigquery.QueryParameter{
		{Name: "uid", Value: userID},
		{Name: "from", Value: from},
		{Name: "to", Value: to},
		{Name: "lim", Value: int64(limit)},
	}
	if keyHash != "" {
		where += " AND LOWER(key_hash) = LOWER(@kh)"
		params = append(params, bigquery.QueryParameter{Name: "kh", Value: keyHash})
	}
	q := s.client.Query(fmt.Sprintf(`
SELECT timestamp, title, artist, key_hash, label
FROM %s
WHERE %s
ORDER BY timestamp DESC
LIMIT @lim
`, s.fq(s.tablePlays), where))
	q.Parameters = params
	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	var out []Play
	for {
		var p Play
		err := it.Next(&p)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ---- helpers ----

func newUserID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "u_" + hex.EncodeToString(b[:]), nil
}

func newAPIKey() (raw string, hash string, err error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(b[:])
	return raw, sha256Hex(raw), nil
}
