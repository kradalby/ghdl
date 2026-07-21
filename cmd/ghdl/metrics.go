package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Health metrics — exposed on /metrics for Prometheus. The download history
// itself lives in SQLite, not Prometheus; these say only whether scraping is
// working, which is what the alerting keys on.
var (
	lastSuccess = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ghdl_last_success_timestamp",
		Help: "Unix time of the last successful collection, per source.",
	}, []string{"source"})

	scrapeErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ghdl_scrape_errors_total",
		Help: "Total collection failures, per source.",
	}, []string{"source"})

	rateRemaining = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ghdl_github_rate_limit_remaining",
		Help: "GitHub API rate-limit budget remaining after the last collection.",
	})

	seriesTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ghdl_series_total",
		Help: "Number of distinct series tracked in the database.",
	})
)
