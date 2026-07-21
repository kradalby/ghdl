package collect

import (
	"context"
	"fmt"
	"net/http"
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

		versDoc, err := getHTML(ctx, g.hc, ghcrVersionsURL(owner, pkg))
		if err != nil {
			return nil, fmt.Errorf("ghcr %s versions: %w", repo, err)
		}
		for _, v := range parseGHCRVersions(versDoc) {
			out = append(out, db.Observation{Source: g.Source(), Repo: repo, Release: v.Label, Count: v.Count})
		}
	}

	return out, nil
}

func ghcrPackageURL(owner, pkg string) string {
	return fmt.Sprintf("https://github.com/%s/%s/pkgs/container/%s", owner, pkg, pkg)
}

func ghcrVersionsURL(owner, pkg string) string {
	return fmt.Sprintf("https://github.com/%s/%s/pkgs/container/%s/versions?filters%%5Bversion_type%%5D=tagged", owner, pkg, pkg)
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
		return 0, fmt.Errorf("Total downloads not found (markup changed?)")
	}
	return total, nil
}

type ghcrVersion struct {
	Label string
	Count int64
}

// parseGHCRVersions extracts per-version pull counts from the versions page.
// Each version is a .Box-row carrying its tags (a.Label), its digest, and a
// "Version downloads" count. The label is the version's tags joined by "," or,
// for untagged builds, a short digest.
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

		label := strings.Join(tags, ",")
		if label == "" {
			if d, ok := row.Find("[value^='sha256:']").Attr("value"); ok {
				label = shortDigest(d)
			}
		}
		if label == "" {
			return
		}

		out = append(out, ghcrVersion{Label: label, Count: count})
	})

	return out
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

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return "sha256:" + d
}
