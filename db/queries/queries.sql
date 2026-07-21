-- name: UpsertSeries :one
-- Get the id of a series, creating it if new. The no-op DO UPDATE lets
-- RETURNING return the existing row's id on conflict (SQLite can't RETURN from
-- a bare DO NOTHING).
INSERT INTO series (source, repo, release, asset, os, arch, format)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source, repo, release, asset)
    DO UPDATE SET source = excluded.source
RETURNING id;

-- name: LatestCount :one
SELECT count
FROM observations
WHERE series_id = ?
ORDER BY ts DESC
LIMIT 1;

-- name: InsertObservation :exec
INSERT INTO observations (series_id, ts, count)
VALUES (?, ?, ?)
ON CONFLICT (series_id, ts) DO NOTHING;

-- name: CountSeries :one
SELECT count(*) FROM series;

-- name: ListSeries :many
-- Series matching optional source/repo filters (pass '' to match all). Used by
-- both /api/series and the timeseries aggregation.
SELECT id, source, repo, release, asset, os, arch, format
FROM series
WHERE (source = sqlc.arg(source) OR sqlc.arg(source) = '')
  AND (repo = sqlc.arg(repo) OR sqlc.arg(repo) = '')
ORDER BY id;

-- name: AnchorObservation :one
-- The last observation at or before `ts`, i.e. the value a step line carries
-- into the start of a query window.
SELECT ts, count
FROM observations
WHERE series_id = sqlc.arg(series_id) AND ts <= sqlc.arg(ts)
ORDER BY ts DESC
LIMIT 1;

-- name: RangeObservations :many
-- Observations strictly after `from` and at or before `to`, in time order.
SELECT ts, count
FROM observations
WHERE series_id = sqlc.arg(series_id)
  AND ts > sqlc.arg(from_ts)
  AND ts <= sqlc.arg(to_ts)
ORDER BY ts;
