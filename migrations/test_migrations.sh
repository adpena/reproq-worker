#!/bin/bash
set -e

# Test that all migrations can be applied to a fresh DB
# Requires DATABASE_URL to be set

if [ -z "$DATABASE_URL" ]; then
    echo "DATABASE_URL is not set"
    exit 1
fi

echo "Testing migrations..."

# Apply all .up.sql files in order
for f in migrations/*.up.sql; do
    echo "Applying $f..."
    psql "$DATABASE_URL" -f "$f" > /dev/null
done

echo "✅ All migrations applied successfully."

# Verify tables exist
TABLES=$(psql "$DATABASE_URL" -t -c "\dt" | awk '{print $3}')

REQUIRED=("task_runs" "periodic_tasks" "reproq_workers" "reproq_queue_controls")

for t in "${REQUIRED[@]}"; do
    if [[ ! " $TABLES " =~ " $t " ]]; then
        echo "❌ Table $t missing!"
        exit 1
    fi
done

echo "✅ Schema verification passed."
