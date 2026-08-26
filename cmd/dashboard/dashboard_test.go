package main

import (
	"encoding/json"
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

// TestVariablesUseInfinityEnvelope pins what silently emptied the dropdowns:
// Infinity reads a {queryType:"infinity", infinityQuery:{…}} envelope and
// treats anything else as a legacy string query, which returns nothing.
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
