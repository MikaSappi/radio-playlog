-- BigQuery schema used by ui-api. Run against the shared dataset.
-- Project: radio-playlog   Dataset: radio_playlog
--
--   bq query --project_id=radio-playlog --use_legacy_sql=false < ui-api/schema.sql
--
-- Or use ./apply-schema.sh next to this file.
--
-- The logger already writes `plays` and reads `api_keys`. This script also
-- manages the UI-owned tables (users, user_settings) and augments api_keys
-- and plays with columns the logger fills in (label snapshot, key_hash on
-- the play row). Safe to re-run.

CREATE TABLE IF NOT EXISTS `radio-playlog.radio_playlog.users` (
  user_id      STRING     NOT NULL,
  email        STRING,
  provider     STRING     NOT NULL,
  provider_sub STRING     NOT NULL,
  created_at   TIMESTAMP  NOT NULL
);

CREATE TABLE IF NOT EXISTS `radio-playlog.radio_playlog.api_keys` (
  key_hash   STRING NOT NULL,
  user_id    STRING NOT NULL,
  enabled    BOOL   NOT NULL,
  label      STRING,
  created_at TIMESTAMP
);

ALTER TABLE `radio-playlog.radio_playlog.api_keys`
  ADD COLUMN IF NOT EXISTS label      STRING,
  ADD COLUMN IF NOT EXISTS created_at TIMESTAMP;

-- plays is created by the logger on first use. We add the per-row
-- key_hash and label snapshot here so ui-api can surface per-key
-- status LEDs and station names in reports without retroactively
-- rewriting old rows.
CREATE TABLE IF NOT EXISTS `radio-playlog.radio_playlog.plays` (
  timestamp TIMESTAMP NOT NULL,
  user_id   STRING    NOT NULL,
  title     STRING,
  artist    STRING,
  key_hash  STRING,
  label     STRING
);

ALTER TABLE `radio-playlog.radio_playlog.plays`
  ADD COLUMN IF NOT EXISTS key_hash STRING,
  ADD COLUMN IF NOT EXISTS label    STRING;

CREATE TABLE IF NOT EXISTS `radio-playlog.radio_playlog.user_settings` (
  user_id       STRING    NOT NULL,
  report_emails STRING,
  cadence       STRING,
  timezone      STRING,
  updated_at    TIMESTAMP NOT NULL
);

ALTER TABLE `radio-playlog.radio_playlog.user_settings`
  ADD COLUMN IF NOT EXISTS timezone STRING;

-- key_alerts tracks silence-detection state so the alerter doesn't
-- spam the user. One row per key.
CREATE TABLE IF NOT EXISTS `radio-playlog.radio_playlog.key_alerts` (
  key_hash        STRING    NOT NULL,
  user_id         STRING    NOT NULL,
  last_seen_at    TIMESTAMP,
  last_alerted_at TIMESTAMP,
  updated_at      TIMESTAMP NOT NULL
);
