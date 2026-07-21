package collect

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kradalby/ghdl/db"
)

// DockerHub collects the repo-wide pull_count for one or more
// "namespace/repository" Docker Hub repositories. Docker Hub's public API
// exposes only the repo total — there is no per-tag or per-arch pull count.
type DockerHub struct {
	repos []string
	hc    *http.Client
}

// NewDockerHub builds a Docker Hub collector.
func NewDockerHub(repos []string) *DockerHub {
	return &DockerHub{repos: repos, hc: http.DefaultClient}
}

// Source implements Collector.
func (d *DockerHub) Source() string { return "dockerhub" }

// Collect fetches pull_count for each configured repository.
func (d *DockerHub) Collect(ctx context.Context) ([]db.Observation, error) {
	var out []db.Observation

	for _, repo := range d.repos {
		url := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/", repo)
		var body struct {
			PullCount int64 `json:"pull_count"`
		}
		if err := getJSON(ctx, d.hc, url, &body); err != nil {
			return nil, fmt.Errorf("dockerhub %s: %w", repo, err)
		}
		out = append(out, db.Observation{Source: d.Source(), Repo: repo, Count: body.PullCount})
	}

	return out, nil
}
