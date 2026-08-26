package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The dashboards are generated, so the meaningful check is that they build
// (Build() schema-validates) and marshal to JSON — the same thing the Nix
// derivation relies on.

func TestDashboardsBuild(t *testing.T) {
	t.Parallel()

	data, err := buildDataDashboard()
	require.NoError(t, err)
	require.Equal(t, "ghdl-downloads", *data.Uid)

	service, err := buildServiceDashboard()
	require.NoError(t, err)
	require.Equal(t, "ghdl-service", *service.Uid)

	for _, d := range []any{data, service} {
		b, err := json.Marshal(d)
		require.NoError(t, err)
		require.NotEmpty(t, b)
	}
}

// TestVariablesUseInfinityEnvelope pins the two things that silently emptied
// the dropdowns: Infinity reads a {queryType:"infinity", infinityQuery:{…}}
// envelope and treats anything else as a legacy string query returning
// nothing; and a blank custom all-value makes Grafana expand All into a list
// of every option instead of the "*" the API reads as "no filter".
func TestVariablesUseInfinityEnvelope(t *testing.T) {
	t.Parallel()

	data, err := buildDataDashboard()
	require.NoError(t, err)

	queries := 0
	for _, v := range data.Templating.List {
		if v.Type != "query" {
			continue
		}
		queries++
		require.NotNil(t, v.AllValue)
		require.Equal(t, "*", *v.AllValue, v.Name)

		require.NotNil(t, v.Query, v.Name)
		q := v.Query.Map
		require.Equal(t, "infinity", q["queryType"], v.Name)
		inner, ok := q["infinityQuery"].(map[string]any)
		require.True(t, ok, v.Name)
		require.Equal(t, "backend", inner["parser"], v.Name)
		require.Contains(t, inner["url"], "/api/values?field=", v.Name)
	}
	require.Equal(t, 3, queries, "version, arch and format")
}

// TestVersionReachesEveryPanel pins the invariant nothing else does: a panel
// that omits the Version variable from its URL is silently deaf to the
// dropdown. Both "by version" panels shipped that way, so the selector could
// not narrow the very panels it exists for.
func TestVersionReachesEveryPanel(t *testing.T) {
	t.Parallel()

	data, err := buildDataDashboard()
	require.NoError(t, err)

	b, err := json.Marshal(data)
	require.NoError(t, err)
	urls := regexp.MustCompile(`/api/timeseries\?[^"]+`).FindAllString(string(b), -1)
	require.Len(t, urls, 7, "six filterable panels plus Docker Hub")

	for _, u := range urls {
		if strings.Contains(u, "source=dockerhub") {
			continue // repo total only; the API exposes nothing to filter
		}
		require.Contains(t, u, "release=${version}", u)
	}
}
