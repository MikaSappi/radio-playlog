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
// and writes. The plays table itself is never touched here — only identity,
// keys, and settings.
type Store struct {
	client       *bigquery.Client
	project      string
	dataset      string
	tableUsers   string
	tableAPIKeys string
	tableSet     string
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
	KeyHash   string    `bigquery:"key_hash"   json:"key_hash"`
	UserID    string    `bigquery:"user_id"    json:"-"`
	Enabled   bool      `bigquery:"enabled"    json:"enabled"`
	Label     string    `bigquery:"label"      json:"label"`
	CreatedAt time.Time `bigquery:"created_at" json:"created_at"`
}

func (s *Store) ListKeys(ctx context.Context, userID string) ([]APIKey, error) {
	q := s.client.Query(fmt.Sprintf(
		"SELECT key_hash, user_id, enabled, label, created_at FROM %s WHERE user_id = @uid ORDER BY created_at DESC",
		s.fq(s.tableAPIKeys),
	))
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

// ---- user settings ----

type Settings struct {
	UserID       string    `bigquery:"user_id"       json:"-"`
	ReportEmails string    `bigquery:"report_emails" json:"-"`
	Cadence      string    `bigquery:"cadence"       json:"cadence"`
	UpdatedAt    time.Time `bigquery:"updated_at"    json:"updated_at"`
}

// GetSettings returns the current settings for a user, or a zero-valued row
// if none exist yet. Callers treat empty Emails / Cadence as unset.
func (s *Store) GetSettings(ctx context.Context, userID string) (*Settings, error) {
	q := s.client.Query(fmt.Sprintf(
		"SELECT user_id, report_emails, cadence, updated_at FROM %s WHERE user_id = @uid LIMIT 1",
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
// this might not work, let's hope for the best
func (s *Store) UpsertSettings(ctx context.Context, userID, emailsJSON, cadence string) error {
	q := s.client.Query(fmt.Sprintf(`
MERGE %s T
USING (SELECT @uid AS user_id, @emails AS report_emails, @cadence AS cadence, CURRENT_TIMESTAMP() AS updated_at) S
ON T.user_id = S.user_id
WHEN MATCHED THEN UPDATE SET report_emails = S.report_emails, cadence = S.cadence, updated_at = S.updated_at
WHEN NOT MATCHED THEN INSERT (user_id, report_emails, cadence, updated_at)
VALUES (S.user_id, S.report_emails, S.cadence, S.updated_at)
`, s.fq(s.tableSet)))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "uid", Value: userID},
		{Name: "emails", Value: emailsJSON},
		{Name: "cadence", Value: cadence},
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
