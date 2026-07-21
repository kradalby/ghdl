package collect

import (
	"context"
	"encoding/json"
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

// Collect fetches the repo total, per-version, and per-version-per-arch pull
// counts for each package.
//
// The web UI gives download counts but no architecture; the container registry
// gives the arch of each sub-manifest but no counts. So we join them: a tagged
// version is a multi-arch index whose sub-manifests appear on the untagged
// versions page (with counts) and in the registry index manifest (with the
// os/arch). The per-arch count is the sub-manifest's own download count — a
// good proxy for per-arch pulls, since a `docker pull` fetches the index plus
// the puller's arch sub-manifest.
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

		tagged, err := g.scrapeVersions(ctx, owner, pkg, "tagged")
		if err != nil {
			return nil, fmt.Errorf("ghcr %s tagged: %w", repo, err)
		}
		untagged, err := g.scrapeVersions(ctx, owner, pkg, "untagged")
		if err != nil {
			return nil, fmt.Errorf("ghcr %s untagged: %w", repo, err)
		}
		subCount := make(map[string]int64, len(untagged))
		for _, u := range untagged {
			subCount[u.Digest] = u.Count
		}

		token, err := g.registryToken(ctx, owner, pkg)
		if err != nil {
			return nil, fmt.Errorf("ghcr %s registry token: %w", repo, err)
		}

		for _, v := range tagged {
			subs, err := g.indexPlatforms(ctx, token, owner, pkg, v.Digest)
			if err != nil {
				return nil, fmt.Errorf("ghcr %s manifest %s: %w", repo, v.Label, err)
			}
			if len(subs) == 0 {
				// Single-arch (or non-index) version: record the total without arch.
				out = append(out, db.Observation{Source: g.Source(), Repo: repo, Release: v.Label, Asset: v.Digest, Count: v.Count})
				continue
			}
			for _, s := range subs {
				out = append(out, db.Observation{
					Source: g.Source(), Repo: repo, Release: v.Label,
					OS: s.OS, Arch: s.Arch, Asset: s.Digest, Count: subCount[s.Digest],
				})
			}
		}
	}

	return out, nil
}

// maxVersionPages caps the GHCR versions walk (~50 versions/page) so a server
// that never returns an empty page can't loop forever.
const maxVersionPages = 200

// scrapeVersions walks every page of the tagged/untagged versions list,
// deduplicating by digest.
func (g *GHCR) scrapeVersions(ctx context.Context, owner, pkg, versionType string) ([]ghcrVersion, error) {
	var out []ghcrVersion
	seen := map[string]bool{}

	for page := 1; page <= maxVersionPages; page++ {
		doc, err := getHTML(ctx, g.hc, ghcrVersionsURL(owner, pkg, versionType, page))
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}
		versions := parseGHCRVersions(doc)
		if len(versions) == 0 {
			break // past the last page
		}
		// Stop when a page repeats what we've seen (last-page-clamps behaviour).
		fresh := false
		for _, v := range versions {
			if v.Digest == "" || seen[v.Digest] {
				continue
			}
			seen[v.Digest] = true
			fresh = true
			out = append(out, v)
		}
		if !fresh {
			break
		}
	}

	return out, nil
}

func ghcrPackageURL(owner, pkg string) string {
	return fmt.Sprintf("https://github.com/%s/%s/pkgs/container/%s", owner, pkg, pkg)
}

func ghcrVersionsURL(owner, pkg, versionType string, page int) string {
	return fmt.Sprintf(
		"https://github.com/%s/%s/pkgs/container/%s/versions?filters%%5Bversion_type%%5D=%s&page=%d",
		owner, pkg, pkg, versionType, page,
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

		digest := rowDigest(row)
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

// digestText matches a full sha256 digest (as rendered in link text on the
// untagged versions page).
var digestText = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// rowDigest extracts a version's sha256 digest. The tagged page carries it in a
// clipboard `value="sha256:…"` attribute; the untagged page renders it as the
// text of the version link. Try both.
func rowDigest(row *goquery.Selection) string {
	if d, ok := row.Find("[value^='sha256:']").Attr("value"); ok {
		return d
	}
	var digest string
	row.Find("a").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		if t := strings.TrimSpace(a.Text()); digestText.MatchString(t) {
			digest = t
			return false
		}
		return true
	})
	return digest
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

// --- container registry (per-arch) ---

const ghcrRegistry = "https://ghcr.io"

// registryToken fetches an anonymous pull token for the package's registry repo.
func (g *GHCR) registryToken(ctx context.Context, owner, pkg string) (string, error) {
	url := fmt.Sprintf("%s/token?service=ghcr.io&scope=repository:%s/%s:pull", ghcrRegistry, owner, pkg)
	var body struct {
		Token string `json:"token"`
	}
	if err := getJSON(ctx, g.hc, url, &body); err != nil {
		return "", err
	}
	return body.Token, nil
}

type subManifest struct {
	Digest string
	OS     string
	Arch   string
}

// indexPlatforms fetches a manifest by reference and, when it is a multi-arch
// index, returns its sub-manifests with normalised os/arch. A single-arch image
// manifest returns nil.
func (g *GHCR) indexPlatforms(ctx context.Context, token, owner, pkg, ref string) ([]subManifest, error) {
	url := fmt.Sprintf("%s/v2/%s/%s/manifests/%s", ghcrRegistry, owner, pkg, ref)
	req, err := newRequest(ctx, url)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))

	resp, err := g.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}

	var idx struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return nil, err
	}

	var out []subManifest
	for _, m := range idx.Manifests {
		// Skip attestation/provenance entries (os/arch "unknown").
		if m.Platform.OS == "" || m.Platform.OS == "unknown" || m.Platform.Architecture == "unknown" {
			continue
		}
		out = append(out, subManifest{
			Digest: m.Digest,
			OS:     m.Platform.OS,
			Arch:   normArch(m.Platform.Architecture, m.Platform.Variant),
		})
	}
	return out, nil
}

// normArch maps a registry architecture+variant to the same labels the release
// filename parser uses, so "by=arch" is consistent across sources.
func normArch(arch, variant string) string {
	if arch == "arm" {
		switch variant {
		case "v7":
			return "armv7"
		case "v6":
			return "armv6"
		case "v5":
			return "armv5"
		}
	}
	return arch // amd64, arm64, 386, …
}
