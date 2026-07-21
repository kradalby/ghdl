package collect

import (
	"context"
	"fmt"

	"github.com/google/go-github/v75/github"

	"github.com/kradalby/ghdl/db"
	"github.com/kradalby/ghdl/parse"
)

// GitHub collects per-asset download counts for the releases of one or more
// "owner/repo" repositories.
type GitHub struct {
	client        *github.Client
	repos         []string
	rateRemaining int
}

// NewGitHub builds a GitHub collector. An empty token works but is subject to
// the low unauthenticated rate limit; a token raises it to 5000 req/hr.
func NewGitHub(token string, repos []string) *GitHub {
	c := github.NewClient(nil)
	if token != "" {
		c = c.WithAuthToken(token)
	}
	return &GitHub{client: c, repos: repos}
}

// Source implements Collector.
func (g *GitHub) Source() string { return "github_release" }

// RateRemaining reports the GitHub API rate-limit budget left after the last
// collection, for the health metric.
func (g *GitHub) RateRemaining() int { return g.rateRemaining }

// Collect lists every release of every configured repo and emits one
// observation per asset, with os/arch/format parsed from the asset filename.
func (g *GitHub) Collect(ctx context.Context) ([]db.Observation, error) {
	var out []db.Observation

	for _, repo := range g.repos {
		owner, name, ok := splitRepo(repo)
		if !ok {
			return nil, fmt.Errorf("invalid github repo %q (want owner/repo)", repo)
		}

		opt := &github.ListOptions{PerPage: 100}
		for {
			releases, resp, err := g.client.Repositories.ListReleases(ctx, owner, name, opt)
			if err != nil {
				return nil, fmt.Errorf("list releases %s: %w", repo, err)
			}
			if resp != nil {
				g.rateRemaining = resp.Rate.Remaining
			}

			for _, rel := range releases {
				tag := rel.GetTagName()
				for _, a := range rel.Assets {
					p := parse.Filename(a.GetName())
					out = append(out, db.Observation{
						Source:  g.Source(),
						Repo:    repo,
						Release: tag,
						Asset:   a.GetName(),
						OS:      p.OS,
						Arch:    p.Arch,
						Format:  p.Format,
						Count:   int64(a.GetDownloadCount()),
					})
				}
			}

			if resp == nil || resp.NextPage == 0 {
				break
			}
			opt.Page = resp.NextPage
		}
	}

	return out, nil
}
