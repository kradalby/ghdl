// Package db is ghdl's persistence layer: a SQLite store of cumulative
// download/pull counts recorded as snapshots over time. Schema migrations run
// through squibble against the embedded schema.sql; typed queries are generated
// by sqlc into db/dbsqlc.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/juanfont/headscale/hscontrol/db/sqliteconfig"
	"github.com/tailscale/squibble"
	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"

	"github.com/kradalby/ghdl/db/dbsqlc"
)

//go:embed schema.sql
var dbSchema string

// schema is the squibble migration target. Updates is empty for v1; schema
// changes append {Source, Target, Apply} rules whose digests chain to the
// hash of schema.sql (see the squibble docs and sfiber for the pattern).
//
// IgnoreTables keeps litestream's bookkeeping tables out of the schema digest —
// litestream (which replicates this db) creates them at runtime, and without
// this squibble would see them as unexpected schema drift and refuse to open.
var schema = &squibble.Schema{
	Current:      dbSchema,
	IgnoreTables: []string{"_litestream_seq", "_litestream_lock"},
}

// DB is the ghdl store.
type DB struct {
	sql *sql.DB
	q   *dbsqlc.Queries
}

// Observation is a single cumulative count for one series at collection time.
// Release/Asset/OS/Arch/Format are empty for repo-wide totals.
type Observation struct {
	Source  string // github_release | dockerhub | ghcr
	Repo    string
	Release string
	Asset   string
	OS      string
	Arch    string
	Format  string
	Count   int64
}

// Point is one aggregated value of a time series at a timestamp.
type Point struct {
	Ts    int64  `json:"ts"`
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// Open opens (creating if needed) the SQLite database at path, applies
// migrations, and returns a ready store.
func Open(ctx context.Context, path string) (*DB, error) {
	uri, err := sqliteconfig.Default(path).ToURL()
	if err != nil {
		return nil, fmt.Errorf("build sqlite uri: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := schema.Apply(ctx, sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return &DB{sql: sqlDB, q: dbsqlc.New(sqlDB)}, nil
}

// Close closes the underlying database.
func (d *DB) Close() error { return d.sql.Close() }

// Save records observations at time ts, writing an observation row only when a
// series' count changed since its last stored value (store-on-change). It
// returns the number of rows actually written.
func (d *DB) Save(ctx context.Context, ts int64, obs []Observation) (int, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	q := d.q.WithTx(tx)
	written := 0

	for _, o := range obs {
		id, err := q.UpsertSeries(ctx, dbsqlc.UpsertSeriesParams{
			Source:  o.Source,
			Repo:    o.Repo,
			Release: o.Release,
			Asset:   o.Asset,
			Os:      o.OS,
			Arch:    o.Arch,
			Format:  o.Format,
		})
		if err != nil {
			return 0, fmt.Errorf("upsert series: %w", err)
		}

		latest, err := q.LatestCount(ctx, id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// first observation for this series
		case err != nil:
			return 0, fmt.Errorf("latest count: %w", err)
		case latest == o.Count:
			continue // unchanged — store-on-change skips it
		}

		if err := q.InsertObservation(ctx, dbsqlc.InsertObservationParams{
			SeriesID: id,
			Ts:       ts,
			Count:    o.Count,
		}); err != nil {
			return 0, fmt.Errorf("insert observation: %w", err)
		}
		written++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return written, nil
}

// Series lists series matching the optional source/repo filters ("" matches
// all).
func (d *DB) Series(ctx context.Context, source, repo string) ([]dbsqlc.Series, error) {
	return d.q.ListSeries(ctx, dbsqlc.ListSeriesParams{Source: source, Repo: repo})
}

// CountSeries returns the number of series (exported for the health metric).
func (d *DB) CountSeries(ctx context.Context) (int64, error) {
	return d.q.CountSeries(ctx)
}

// Filter selects series by any combination of dimensions; an empty field
// matches anything. It is the basis for drill-down (e.g. release="0.29.2" +
// by="arch").
type Filter struct {
	Source, Repo, Release, OS, Arch, Format string
}

func (f Filter) matches(s dbsqlc.Series) bool {
	return (f.Release == "" || s.Release == f.Release) &&
		(f.OS == "" || s.Os == f.OS) &&
		(f.Arch == "" || s.Arch == f.Arch) &&
		(f.Format == "" || s.Format == f.Format)
}

// TimeSeries returns the download counts of the series matching the filter,
// aggregated into step-function lines grouped by `by` (one of
// os/arch/format/release/asset; anything else means a single combined line).
// Because counts are stored on-change, values are carried forward: each output
// point sums every member series' last-known value at that timestamp.
//
// The result is a dense long-format table — every label gets a point at every
// timestamp, ordered by timestamp then label. Grafana's Infinity datasource
// only splits a long frame into one line per label via data.LongToWide, which
// refuses a frame whose time column is not sorted ascending; a label-major or
// ragged result silently stays one flat "count" line that saws up and down.
func (d *DB) TimeSeries(ctx context.Context, f Filter, by string, from, to int64) ([]Point, error) {
	series, err := d.Series(ctx, f.Source, f.Repo)
	if err != nil {
		return nil, err
	}

	// group label -> series ids; and per-series points within the window.
	groups := map[string][]int64{}
	pointsFor := map[int64][]Point{} // ts,count per series (Label unused here)
	tsSet := map[int64]struct{}{}

	for _, s := range series {
		if !f.matches(s) {
			continue
		}
		pts, err := d.windowPoints(ctx, s.ID, from, to)
		if err != nil {
			return nil, err
		}
		if len(pts) == 0 {
			continue // no data in or before the window
		}
		groups[groupLabel(s, by)] = append(groups[groupLabel(s, by)], s.ID)
		pointsFor[s.ID] = pts
		for _, p := range pts {
			tsSet[p.Ts] = struct{}{}
		}
	}
	if len(tsSet) == 0 {
		return nil, nil
	}

	timestamps := slices.Sorted(maps.Keys(tsSet))
	// Carry every line to the end of the window, so a release that stopped
	// changing still draws a full-width step instead of a single point.
	if last := timestamps[len(timestamps)-1]; last < to {
		timestamps = append(timestamps, to)
	}
	labels := slices.Sorted(maps.Keys(groups))

	// Per-series cursor into pointsFor, advanced as the shared timeline moves;
	// cur holds each series' carried-forward value (0 before its first point).
	idx := make(map[int64]int, len(pointsFor))
	cur := make(map[int64]int64, len(pointsFor))

	out := make([]Point, 0, len(timestamps)*len(labels))
	for _, ts := range timestamps {
		for _, label := range labels {
			var sum int64
			for _, id := range groups[label] {
				pts := pointsFor[id]
				for idx[id] < len(pts) && pts[idx[id]].Ts <= ts {
					cur[id] = pts[idx[id]].Count
					idx[id]++
				}
				sum += cur[id]
			}
			out = append(out, Point{Ts: ts, Label: label, Count: sum})
		}
	}

	return out, nil
}

// windowPoints returns a series' step-function points across [from, to]: the
// carried-in value at `from` (if any), then every change within (from, to].
func (d *DB) windowPoints(ctx context.Context, id, from, to int64) ([]Point, error) {
	var pts []Point

	anchor, err := d.q.AnchorObservation(ctx, dbsqlc.AnchorObservationParams{SeriesID: id, Ts: from})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// no value before the window; the line starts at its first change
	case err != nil:
		return nil, fmt.Errorf("anchor: %w", err)
	default:
		pts = append(pts, Point{Ts: from, Count: anchor.Count})
	}

	rows, err := d.q.RangeObservations(ctx, dbsqlc.RangeObservationsParams{SeriesID: id, FromTs: from, ToTs: to})
	if err != nil {
		return nil, fmt.Errorf("range: %w", err)
	}
	for _, r := range rows {
		pts = append(pts, Point{Ts: r.Ts, Count: r.Count})
	}

	return pts, nil
}

// dimension reads one of a series' dimension columns; an unknown field yields
// "".
func dimension(s dbsqlc.Series, field string) string {
	switch field {
	case "source":
		return s.Source
	case "repo":
		return s.Repo
	case "release":
		return s.Release
	case "asset":
		return s.Asset
	case "os":
		return s.Os
	case "arch":
		return s.Arch
	case "format":
		return s.Format
	default:
		return ""
	}
}

// groupLabel names the step line a series belongs to under the given grouping.
// Every line gets a non-empty name: a series with no value for the dimension
// (a checksums file has no arch) lands on "unknown", and a `by` that is not a
// dimension puts everything on one "total" line. An empty name would reach
// Grafana as an unnamed series and render as a bare "count" in the legend.
func groupLabel(s dbsqlc.Series, by string) string {
	switch by {
	case "source", "repo", "release", "asset", "os", "arch", "format":
		if v := dimension(s, by); v != "" {
			return v
		}
		return "unknown"
	default:
		return "total"
	}
}

// Values returns the distinct non-empty values of `field` among series matching
// the filter, sorted — for Grafana template-variable dropdowns (e.g. the list
// of release versions for a source).
func (d *DB) Values(ctx context.Context, f Filter, field string) ([]string, error) {
	series, err := d.Series(ctx, f.Source, f.Repo)
	if err != nil {
		return nil, err
	}

	set := map[string]struct{}{}
	for _, s := range series {
		if !f.matches(s) {
			continue
		}
		if v := dimension(s, field); v != "" {
			set[v] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(set)), nil
}
