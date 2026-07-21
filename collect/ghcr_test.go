package collect

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The GHCR parsers scrape GitHub's package web UI, which has no API contract.
// These fixture-backed tests are the tripwire: if GitHub changes the markup,
// they fail loudly instead of the collector silently recording nothing.

func TestParseGHCRTotal(t *testing.T) {
	t.Parallel()

	f, err := os.Open("testdata/ghcr_package.html")
	require.NoError(t, err)
	defer f.Close()

	doc, err := docFromReader(f)
	require.NoError(t, err)

	total, err := parseGHCRTotal(doc)
	require.NoError(t, err)
	require.Equal(t, int64(1418338), total, "total downloads from the saved package page")
}

func TestParseGHCRVersions(t *testing.T) {
	t.Parallel()

	f, err := os.Open("testdata/ghcr_versions.html")
	require.NoError(t, err)
	defer f.Close()

	doc, err := docFromReader(f)
	require.NoError(t, err)

	versions := parseGHCRVersions(doc)
	require.Len(t, versions, 50, "one entry per .Box-row version card")

	// The tagged versions page carries a 'latest'-labelled row.
	var haveLatest bool
	for _, v := range versions {
		if v.Label == "" {
			t.Errorf("empty version label: %+v", v)
		}
		if containsTag(v.Label, "latest") {
			haveLatest = true
		}
	}
	require.True(t, haveLatest, "a version labelled 'latest' should be present")
}

func TestParseNum(t *testing.T) {
	t.Parallel()
	require.Equal(t, int64(1418338), parseNum("1,418,338"))
	require.Equal(t, int64(0), parseNum("0"))
	require.Equal(t, int64(41918), parseNum("  41,918 "))
	require.Equal(t, int64(0), parseNum(""))
}

func containsTag(label, tag string) bool {
	for _, t := range splitTags(label) {
		if t == tag {
			return true
		}
	}
	return false
}

func splitTags(label string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(label); i++ {
		if i == len(label) || label[i] == ',' {
			out = append(out, label[start:i])
			start = i + 1
		}
	}
	return out
}
