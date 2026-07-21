package collect

import (
	"os"
	"regexp"
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

	semver := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	var haveSemver bool
	for _, v := range versions {
		require.NotEmpty(t, v.Digest, "each version has a digest: %+v", v)
		require.NotContains(t, v.Label, ",", "labels are a single version, not joined tags")
		require.NotEmpty(t, v.Label)
		if semver.MatchString(v.Label) {
			haveSemver = true
		}
	}
	require.True(t, haveSemver, "at least one major.minor.patch label")
}

func TestParseNum(t *testing.T) {
	t.Parallel()
	require.Equal(t, int64(1418338), parseNum("1,418,338"))
	require.Equal(t, int64(0), parseNum("0"))
	require.Equal(t, int64(41918), parseNum("  41,918 "))
	require.Equal(t, int64(0), parseNum(""))
}

func TestNormArch(t *testing.T) {
	t.Parallel()
	require.Equal(t, "amd64", normArch("amd64", ""))
	require.Equal(t, "arm64", normArch("arm64", "v8"))
	require.Equal(t, "armv7", normArch("arm", "v7"))
	require.Equal(t, "armv6", normArch("arm", "v6"))
	require.Equal(t, "386", normArch("386", ""))
}

func TestVersionLabel(t *testing.T) {
	t.Parallel()
	require.Equal(t, "0.29.2", versionLabel([]string{"latest", "sha-8eea8948", "v0.29.2", "0.29.2", "v0.29", "v0"}))
	require.Equal(t, "0.28.0", versionLabel([]string{"sha-97fa117c", "v0.28", "v0.28.0", "0.28", "0.28.0"}))
	require.Equal(t, "development", versionLabel([]string{"main-f20f1f1", "development"}))
	require.Equal(t, "untagged", versionLabel([]string{"sha-abc123", "main-def456"}))
	require.Equal(t, "untagged", versionLabel(nil))
}
