-- BigQuery schema used by ui-api. Run against the shared dataset.
-- Project: radio-playlog   Dataset: radio_playlog
--
--   bq query --project_id=radio-playlog --use_legacy_sql=false < ui-api/schema.sql
--
-- Or use ./apply-schema.sh next to this file.
--
-- The logger already writes `plays` and reads `api_keys`. This script only
-- adds the UI-owned tables (users, user_settings) and, for api_keys, adds
-- two columns the logger ignores (label, created_at). Safe to re-run.

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

CREATE TABLE IF NOT EXISTS `radio-playlog.radio_playlog.user_settings` (
  user_id       STRING    NOT NULL,
  report_emails STRING,
  cadence       STRING,
  updated_at    TIMESTAMP NOT NULL
);
