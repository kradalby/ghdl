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

	var (
		haveSemver bool
		nonZero    int
	)

	for _, v := range versions {
		require.NotEmpty(t, v.Digest, "each version has a digest: %+v", v)
		require.NotContains(t, v.Label, ",", "labels are a single version, not joined tags")
		require.NotEmpty(t, v.Label)

		if semver.MatchString(v.Label) {
			haveSemver = true
		}

		if v.Count != 0 {
			nonZero++
		}
	}

	require.True(t, haveSemver, "at least one major.minor.patch label")
	// Counts are the point of the scrape, so assert they parse. Without this the
	// suite stayed green even if every count came back 0.
	require.Positive(t, nonZero, "some versions have a non-zero download count")
}

func TestParseNum(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want int64
	}{
		{"1,418,338", 1418338},
		{"0", 0},
		{"  41,918 ", 41918},
		{"\n        \n    \n\n        120\n        \n      ", 120},
	} {
		got, err := parseNum(tt.in)
		require.NoError(t, err, "parseNum(%q)", tt.in)
		require.Equal(t, tt.want, got, "parseNum(%q)", tt.in)
	}

	// Anything that is not a plain integer must fail rather than truncate.
	// "1.42M" silently became 1 before, which is worse than no count at all:
	// it is a plausible number that corrupts the series permanently.
	for _, in := range []string{"", "1.42M", "1.4k", "n/a", "12 downloads"} {
		_, err := parseNum(in)
		require.ErrorIs(t, err, errNotANumber, "parseNum(%q) must not truncate", in)
	}
}

// TestParseGHCRUntagged guards the untagged page, which renders the digest as
// link text (not a value= attribute like the tagged page) — the per-arch join
// depends on extracting it.
func TestParseGHCRUntagged(t *testing.T) {
	t.Parallel()

	f, err := os.Open("testdata/ghcr_untagged.html")
	require.NoError(t, err)
	defer f.Close()

	doc, err := docFromReader(f)
	require.NoError(t, err)

	versions := parseGHCRVersions(doc)
	require.Len(t, versions, 50)

	nonZero := 0

	for _, v := range versions {
		require.Regexp(t, `^sha256:[0-9a-f]{64}$`, v.Digest, "untagged digest from link text")

		if v.Count != 0 {
			nonZero++
		}
	}

	require.Positive(t, nonZero, "some sub-manifests have a non-zero download count")
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

	// Prereleases: both headscale's tag spellings, and a debug image, which is
	// a prerelease as far as semver is concerned.
	require.Equal(t, "0.29.0-beta.4", versionLabel([]string{"sha-de9db9c", "v0.29.0-beta.4", "0.29.0-beta.4"}))
	require.Equal(t, "0.13.0-beta1", versionLabel([]string{"0.13.0-beta1", "sha-abc123"}))
	require.Equal(t, "0.29.3-debug", versionLabel([]string{"latest-debug", "0.29-debug", "v0.29.3-debug", "0.29.3-debug", "0-debug"}))

	// A plain tag still wins over a prerelease one on the same version.
	require.Equal(t, "0.29.3", versionLabel([]string{"v0.29.3-rc.1", "0.29.3", "latest"}))

	// Build and backup tags are still not versions.
	require.Equal(t, "untagged", versionLabel([]string{"sha-01b85e5-debug", "main-036760d"}))
	require.Equal(t, "untagged", versionLabel([]string{"1-ogen-backup-0e77a212b"}))
}
