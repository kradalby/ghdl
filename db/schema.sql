-- ghdl schema. Source of truth for squibble: the live database is migrated to
-- match this file exactly (see db.go). Cumulative download/pull counts are
-- stored as snapshots; a row is written only when a count changes
-- (store-on-change), so frozen releases stop producing rows.

-- series is one tracked counter: a source (github_release/dockerhub/ghcr), a
-- repo, and — where the source exposes it — a specific release and asset with
-- the os/arch/format parsed out of the asset filename. Repo-wide totals
-- (Docker Hub pull_count, GHCR total) leave release/asset empty. The columns
-- are NOT NULL DEFAULT '' rather than nullable because SQLite treats NULLs as
-- distinct in a UNIQUE index, which would defeat dedup for the empty-keyed
-- repo-total rows.
CREATE TABLE series (
    id      INTEGER PRIMARY KEY,
    source  TEXT NOT NULL,
    repo    TEXT NOT NULL,
    release TEXT NOT NULL DEFAULT '',
    asset   TEXT NOT NULL DEFAULT '',
    os      TEXT NOT NULL DEFAULT '',
    arch    TEXT NOT NULL DEFAULT '',
    format  TEXT NOT NULL DEFAULT '',
    UNIQUE (source, repo, release, asset)
);

-- observations is the time series: the cumulative count of a series at a point
-- in time. WITHOUT ROWID keeps the (series_id, ts) primary key as the physical
-- storage, so range scans over one series are a contiguous index walk.
CREATE TABLE observations (
    series_id INTEGER NOT NULL REFERENCES series (id),
    ts        INTEGER NOT NULL,
    count     INTEGER NOT NULL,
    PRIMARY KEY (series_id, ts)
) WITHOUT ROWID;
