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
