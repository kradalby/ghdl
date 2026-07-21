package collect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/kradalby/ghdl/db"
)

// GHCR collects container pull counts for one or more "owner/package" GHCR
// packages. GitHub exposes no official download-count API for containers
// (github/community#146215), so ghdl scrapes the package web UI:
//   - the package page for the repo-wide "Total downloads", and
//   - the versions page for per-version counts.
//
// TODO(ghcr): replace this HTML scrape with the official API when
// github/community#146215 ships. The fixture-backed parser tests fail loudly if
// GitHub changes the markup, which is the signal to revisit.
type GHCR struct {
	repos []string
	hc    *http.Client
}

// NewGHCR builds a GHCR collector.
func NewGHCR(repos []string) *GHCR {
	return &GHCR{repos: repos, hc: http.DefaultClient}
}

// Source implements Collector.
func (g *GHCR) Source() string { return "ghcr" }

// Collect fetches the total and per-version pull counts for each package.
func (g *GHCR) Collect(ctx context.Context) ([]db.Observation, error) {
	var out []db.Observation

	for _, repo := range g.repos {
		owner, pkg, ok := splitRepo(repo)
		if !ok {
			return nil, fmt.Errorf("invalid ghcr repo %q (want owner/package)", repo)
		}

		totalDoc, err := getHTML(ctx, g.hc, ghcrPackageURL(owner, pkg))
		if err != nil {
			return nil, fmt.Errorf("ghcr %s total: %w", repo, err)
		}
		total, err := parseGHCRTotal(totalDoc)
		if err != nil {
			return nil, fmt.Errorf("ghcr %s total: %w", repo, err)
		}
		out = append(out, db.Observation{Source: g.Source(), Repo: repo, Count: total})

		// The versions page is paginated (~50 per page); walk every page so
		// historic versions are captured, not just the most recent. Each series
		// is keyed by digest (unique per version) with a major.minor.patch
		// release label, so re-pushes of a tag sum under the same version in the
		// graph rather than colliding into one flip-flopping counter.
		seen := map[string]bool{}
		for page := 1; page <= maxVersionPages; page++ {
			versDoc, err := getHTML(ctx, g.hc, ghcrVersionsURL(owner, pkg, page))
			if err != nil {
				return nil, fmt.Errorf("ghcr %s versions page %d: %w", repo, page, err)
			}
			versions := parseGHCRVersions(versDoc)
			if len(versions) == 0 {
				break // past the last page
			}
			// Stop if a page repeats what we've already seen (defensive against a
			// last-page-clamps-to-last-page server behaviour).
			fresh := false
			for _, v := range versions {
				if seen[v.Digest] {
					continue
				}
				seen[v.Digest] = true
				fresh = true
				out = append(out, db.Observation{Source: g.Source(), Repo: repo, Release: v.Label, Asset: v.Digest, Count: v.Count})
			}
			if !fresh {
				break
			}
		}
	}

	return out, nil
}

// maxVersionPages caps the GHCR versions walk (~50 versions/page) so a server
// that never returns an empty page can't loop forever.
const maxVersionPages = 200

func ghcrPackageURL(owner, pkg string) string {
	return fmt.Sprintf("https://github.com/%s/%s/pkgs/container/%s", owner, pkg, pkg)
}

func ghcrVersionsURL(owner, pkg string, page int) string {
	return fmt.Sprintf(
		"https://github.com/%s/%s/pkgs/container/%s/versions?filters%%5Bversion_type%%5D=tagged&page=%d",
		owner, pkg, pkg, page,
	)
}

// parseGHCRTotal extracts the repo-wide "Total downloads" number, which renders
// as an <h3 title="N"> sibling of a <span>Total downloads</span>.
func parseGHCRTotal(doc *goquery.Document) (int64, error) {
	var total int64
	found := false
	doc.Find("span").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if strings.TrimSpace(s.Text()) != "Total downloads" {
			return true
		}
		if t, ok := s.Parent().Find("h3").Attr("title"); ok {
			total = parseNum(t)
			found = true
			return false
		}
		return true
	})
	if !found {
		return 0, errors.New("total downloads not found (markup changed?)")
	}
	return total, nil
}

type ghcrVersion struct {
	Label  string // major.minor.patch, or a channel tag / "untagged"
	Digest string // sha256:... — unique per version
	Count  int64
}

// parseGHCRVersions extracts per-version pull counts from the versions page.
// Each version is a .Box-row carrying its tags (a.Label), its digest, and a
// "Version downloads" count.
func parseGHCRVersions(doc *goquery.Document) []ghcrVersion {
	var out []ghcrVersion

	doc.Find(".Box-row").Each(func(_ int, row *goquery.Selection) {
		count, ok := versionDownloads(row)
		if !ok {
			return
		}

		var tags []string
		row.Find("a.Label").Each(func(_ int, a *goquery.Selection) {
			if t := strings.TrimSpace(a.Text()); t != "" {
				tags = append(tags, t)
			}
		})

		digest, _ := row.Find("[value^='sha256:']").Attr("value")
		if len(tags) == 0 && digest == "" {
			return
		}

		out = append(out, ghcrVersion{Label: versionLabel(tags), Digest: digest, Count: count})
	})

	return out
}

// semverTag matches a bare major.minor.patch tag, optionally v-prefixed.
var semverTag = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)

// versionLabel picks a major.minor.patch label from a version's tags, preferring
// a semver tag (normalised without a leading "v"); failing that a human channel
// tag (e.g. "development", "stable", "latest"), else "untagged". Build tags like
// "sha-1a2b3c" and "main-1a2b3c" are skipped.
func versionLabel(tags []string) string {
	for _, t := range tags {
		if m := semverTag.FindStringSubmatch(t); m != nil {
			return m[1]
		}
	}
	for _, t := range tags {
		if !strings.HasPrefix(t, "sha-") && !strings.ContainsRune(t, '-') {
			return t
		}
	}
	return "untagged"
}

// versionDownloads pulls the count out of a version row's "Version downloads"
// element, whose parent span reads like "1,234 Version downloads".
func versionDownloads(row *goquery.Selection) (int64, bool) {
	var count int64
	found := false
	row.Find("span.sr-only").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if strings.TrimSpace(s.Text()) != "Version downloads" {
			return true
		}
		text := strings.Replace(s.Parent().Text(), "Version downloads", "", 1)
		count = parseNum(text)
		found = true
		return false
	})
	return count, found
}
