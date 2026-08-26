package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestStoreOnChange is the load-bearing invariant: an unchanged count must not
// write a new observation row, a changed one must.
func TestStoreOnChange(t *testing.T) {
	t.Parallel()
	d := openTest(t)
	ctx := context.Background()

	obs := []Observation{{Source: "github_release", Repo: "juanfont/headscale", Release: "0.26.0", Asset: "headscale_0.26.0_linux_amd64.deb", OS: "linux", Arch: "amd64", Format: "deb", Count: 100}}

	n, err := d.Save(ctx, 1000, obs)
	require.NoError(t, err)
	require.Equal(t, 1, n, "first observation is written")

	n, err = d.Save(ctx, 2000, obs)
	require.NoError(t, err)
	require.Equal(t, 0, n, "unchanged count writes nothing")

	obs[0].Count = 150
	n, err = d.Save(ctx, 3000, obs)
	require.NoError(t, err)
	require.Equal(t, 1, n, "changed count writes one row")

	// Series is deduped, not duplicated, across saves.
	series, err := d.Series(ctx, "", "")
	require.NoError(t, err)
	require.Len(t, series, 1)
}

// TestRepoTotalDedup guards the empty-key case: Docker Hub / GHCR repo-totals
// (empty release/asset) must dedup to one series despite SQLite's NULL-distinct
// UNIQUE behaviour — which is why the columns are NOT NULL DEFAULT ”.
func TestRepoTotalDedup(t *testing.T) {
	t.Parallel()
	d := openTest(t)
	ctx := context.Background()

	total := []Observation{{Source: "dockerhub", Repo: "headscale/headscale", Count: 3_800_000}}
	_, err := d.Save(ctx, 1000, total)
	require.NoError(t, err)
	total[0].Count = 3_900_000
	_, err = d.Save(ctx, 2000, total)
	require.NoError(t, err)

	series, err := d.Series(ctx, "dockerhub", "")
	require.NoError(t, err)
	require.Len(t, series, 1, "one repo-total series, not one per scrape")
}

// TestTimeSeriesAggregation checks carry-forward: two arm64 series changing at
// different times must sum as step functions (each holds its last value).
func TestTimeSeriesAggregation(t *testing.T) {
	t.Parallel()
	d := openTest(t)
	ctx := context.Background()

	mk := func(asset string, count int64) Observation {
		return Observation{Source: "github_release", Repo: "r", Release: "1", Asset: asset, OS: "linux", Arch: "arm64", Format: "deb", Count: count}
	}

	require.NoError(t, must(d.Save(ctx, 100, []Observation{mk("a", 10), mk("b", 5)})))
	require.NoError(t, must(d.Save(ctx, 200, []Observation{mk("a", 10), mk("b", 8)}))) // only b changes
	require.NoError(t, must(d.Save(ctx, 300, []Observation{mk("a", 12), mk("b", 8)}))) // only a changes

	pts, err := d.TimeSeries(ctx, Filter{Source: "github_release", Repo: "r"}, "arch", 0, 1000)
	require.NoError(t, err)

	// All series share arch=arm64 → one line. Sums by carried value:
	//   t=100: a=10,b=5  -> 15
	//   t=200: a=10,b=8  -> 18
	//   t=300: a=12,b=8  -> 20
	got := map[int64]int64{}
	for _, p := range pts {
		require.Equal(t, "arm64", p.Label)
		got[p.Ts] = p.Count
	}
	require.Equal(t, int64(15), got[100])
	require.Equal(t, int64(18), got[200])
	require.Equal(t, int64(20), got[300])
}

// TestFilterDrilldown checks that a pinned dimension (release) restricts the
// series before grouping by another (arch) — the drill-down path.
func TestFilterDrilldown(t *testing.T) {
	t.Parallel()
	d := openTest(t)
	ctx := context.Background()

	obs := []Observation{
		{Source: "github_release", Repo: "r", Release: "1.0.0", Asset: "a", OS: "linux", Arch: "amd64", Format: "deb", Count: 100},
		{Source: "github_release", Repo: "r", Release: "1.0.0", Asset: "b", OS: "linux", Arch: "arm64", Format: "deb", Count: 40},
		{Source: "github_release", Repo: "r", Release: "0.9.0", Asset: "c", OS: "linux", Arch: "amd64", Format: "deb", Count: 999},
	}
	require.NoError(t, must(d.Save(ctx, 100, obs)))

	// Pin release=1.0.0, split by arch → only 1.0.0's two arches, not 0.9.0.
	pts, err := d.TimeSeries(ctx, Filter{Source: "github_release", Repo: "r", Release: "1.0.0"}, "arch", 0, 1000)
	require.NoError(t, err)
	got := map[string]int64{}
	for _, p := range pts {
		got[p.Label] = p.Count
	}
	require.Equal(t, int64(100), got["amd64"])
	require.Equal(t, int64(40), got["arm64"])
	require.NotContains(t, got, "", "0.9.0's amd64 (999) must be excluded")

	// Values(release) lists both versions; scoped by arch=arm64 lists only 1.0.0.
	rels, err := d.Values(ctx, Filter{Source: "github_release", Repo: "r"}, "release")
	require.NoError(t, err)
	require.Equal(t, []string{"0.9.0", "1.0.0"}, rels)

	arm, err := d.Values(ctx, Filter{Source: "github_release", Repo: "r", Arch: "arm64"}, "release")
	require.NoError(t, err)
	require.Equal(t, []string{"1.0.0"}, arm)
}

// TestOpenIgnoresLitestreamTables reproduces the deploy blocker: litestream
// adds _litestream_seq/_litestream_lock to the db, and squibble must not treat
// them as schema drift on the next open.
func TestOpenIgnoresLitestreamTables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ls.db")

	d, err := Open(ctx, path)
	require.NoError(t, err)
	_, err = d.sql.ExecContext(ctx, `CREATE TABLE _litestream_seq (id INTEGER PRIMARY KEY, seq INTEGER);
CREATE TABLE _litestream_lock (id INTEGER);`)
	require.NoError(t, err)
	require.NoError(t, d.Close())

	// Reopen: squibble applies its digest check and must ignore the litestream tables.
	d2, err := Open(ctx, path)
	require.NoError(t, err, "squibble must ignore litestream tables")
	require.NoError(t, d2.Close())
}

func must(_ int, err error) error { return err }

// TestTimeSeriesLongFormat guards the shape Grafana needs, not just the sums.
// Infinity turns the long {ts,label,count} table into one line per label with
// data.LongToWide, which refuses a frame whose time column is not sorted
// ascending; a label-major result silently renders as one sawtoothing "count"
// line. Labels must also be non-empty, or the line has no name in the legend.
func TestTimeSeriesLongFormat(t *testing.T) {
	t.Parallel()
	d := openTest(t)
	ctx := context.Background()

	mk := func(rel, arch string, count int64) Observation {
		return Observation{Source: "github_release", Repo: "r", Release: rel, Asset: rel + arch, OS: "linux", Arch: arch, Format: "deb", Count: count}
	}
	// checksums.txt has no arch — it must still get a named line.
	sums := Observation{Source: "github_release", Repo: "r", Release: "1", Asset: "checksums.txt", Count: 7}

	require.NoError(t, must(d.Save(ctx, 100, []Observation{mk("1", "amd64", 10), mk("1", "arm64", 5), sums})))
	require.NoError(t, must(d.Save(ctx, 200, []Observation{mk("1", "amd64", 20), mk("1", "arm64", 5), sums})))
	// A new release appears late; the old lines must still have points here.
	require.NoError(t, must(d.Save(ctx, 300, []Observation{mk("2", "amd64", 3)})))

	pts, err := d.TimeSeries(ctx, Filter{Source: "github_release", Repo: "r"}, "arch", 0, 400)
	require.NoError(t, err)
	require.NotEmpty(t, pts)

	labels := map[string]int{}
	for i, p := range pts {
		require.NotEmpty(t, p.Label, "every line needs a name")
		if i > 0 {
			require.GreaterOrEqual(t, p.Ts, pts[i-1].Ts, "points must be ordered by time, not grouped by label")
		}
		labels[p.Label]++
	}

	// Dense: every label has a point at every timestamp (100, 200, 300 and the
	// window end 400), so no line is a lone unconnected point.
	require.Equal(t, map[string]int{"amd64": 4, "arm64": 4, "unknown": 4}, labels)

	// The window end repeats the last known value rather than inventing one.
	last := map[string]int64{}
	for _, p := range pts {
		if p.Ts == 400 {
			last[p.Label] = p.Count
		}
	}
	require.Equal(t, int64(23), last["amd64"], "20 from release 1 + 3 from release 2")
	require.Equal(t, int64(5), last["arm64"])
	require.Equal(t, int64(7), last["unknown"])

	// An unrecognised `by` collapses everything onto one named line.
	total, err := d.TimeSeries(ctx, Filter{Source: "github_release", Repo: "r"}, "none", 0, 400)
	require.NoError(t, err)
	for _, p := range total {
		require.Equal(t, "total", p.Label)
	}
}
