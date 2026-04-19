#!/usr/bin/env bash
# Applies schema.sql to BigQuery. Should be reasonably safe to re-run.
set -euo pipefail

PROJECT="${GCP_PROJECT:-radio-playlog}"
DATASET="${BQ_DATASET:-radio_playlog}"

cd "$(dirname "$0")"

# Ensure the dataset exists before creating tables in it.
bq --project_id="$PROJECT" --location=EU mk -f --dataset "$PROJECT:$DATASET" >/dev/null

bq --project_id="$PROJECT" query --use_legacy_sql=false --format=none < schema.sql

echo "Schema applied to $PROJECT:$DATASET"
