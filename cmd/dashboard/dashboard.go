// Command dashboard emits ghdl's Grafana dashboards as dashboard-model JSON,
// built with the Grafana Foundation SDK. It is a build-time generator: the Nix
// derivation (or anyone not using Nix) runs it and captures the output.
//
// Two dashboards are produced:
//   - ghdl.json          — the data: download/pull counts over time, read from
//     ghdl's JSON API through the Infinity datasource.
//   - ghdl-service.json  — the service: scrape health from Prometheus.
//
// Both reference their datasource through a template variable rather than a
// hardcoded UID, so they drop into any Grafana that has the datasources.
package main

import (
	"github.com/grafana/grafana-foundation-sdk/go/cog/variants"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
	"github.com/grafana/grafana-foundation-sdk/go/stat"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
	"github.com/grafana/grafana-foundation-sdk/go/units"
)

// The primary target. The data dashboard is headscale-specific in its repo
// identifiers because each source names the project differently.
const (
	promVar     = "datasource" // Prometheus datasource variable
	infinityVar = "ghdl"       // Infinity datasource variable

	ghRepo     = "juanfont/headscale"  // GitHub releases + GHCR
	dockerRepo = "headscale/headscale" // Docker Hub
)

func new[T any](v T) *T { return &v }

// legend configures a panel legend. Per the project convention, every panel
// shows a legend; multi-series panels get a table with last/max columns.
func legend(multi bool) *common.VizLegendOptionsBuilder {
	l := common.NewVizLegendOptionsBuilder().
		Placement(common.LegendPlacementBottom).
		ShowLegend(true)
	if multi {
		return l.DisplayMode(common.LegendDisplayModeTable).Calcs([]string{"lastNotNull", "max"})
	}
	return l.DisplayMode(common.LegendDisplayModeList)
}

// --- Prometheus (service dashboard) helpers ---

func promDS() common.DataSourceRef {
	return common.DataSourceRef{Type: new("prometheus"), Uid: new("${" + promVar + "}")}
}

func promQuery(expr, legendFmt string) *prometheus.DataqueryBuilder {
	return prometheus.NewDataqueryBuilder().Expr(expr).LegendFormat(legendFmt)
}

func promTimeseries(title, desc, unit string, multi bool, targets ...*prometheus.DataqueryBuilder) *timeseries.PanelBuilder {
	p := timeseries.NewPanelBuilder().
		Title(title).Description(desc).Unit(unit).
		Datasource(promDS()).Span(12).Height(8).FillOpacity(10).
		Legend(legend(multi))
	for _, t := range targets {
		p = p.WithTarget(t)
	}
	return p
}

func promStat(title, desc, unit, expr, legendFmt string) *stat.PanelBuilder {
	return stat.NewPanelBuilder().
		Title(title).Description(desc).Unit(unit).
		Datasource(promDS()).Span(6).Height(4).
		ReduceOptions(common.NewReduceDataOptionsBuilder().Calcs([]string{"lastNotNull"})).
		WithTarget(promQuery(expr, legendFmt))
}

// --- Infinity (data dashboard) helpers ---

func infinityDS() common.DataSourceRef {
	return common.DataSourceRef{Type: new("yesoreyeram-infinity-datasource"), Uid: new("${" + infinityVar + "}")}
}

// infinityTarget builds an Infinity JSON-over-URL query against ghdl's
// /api/timeseries, mapping the {ts,label,count} rows into a time series. The
// URL is relative; the Infinity datasource carries ghdl's base URL.
func infinityTarget(refID, url string) *variants.UnknownDataqueryBuilder {
	return variants.NewUnknownDataqueryBuilderFromObject(variants.UnknownDataquery{
		"refId":         refID,
		"datasource":    map[string]any{"type": "yesoreyeram-infinity-datasource", "uid": "${" + infinityVar + "}"},
		"type":          "json",
		"source":        "url",
		"format":        "timeseries",
		"url":           url,
		"url_options":   map[string]any{"method": "GET"},
		"root_selector": "",
		"columns": []map[string]any{
			{"selector": "ts", "text": "time", "type": "timestamp_epoch_s"},
			{"selector": "label", "text": "label", "type": "string"},
			{"selector": "count", "text": "count", "type": "number"},
		},
	})
}

// tsURL builds a /api/timeseries URL with Grafana's time-range macros (seconds).
func tsURL(source, repo, by string) string {
	return "/api/timeseries?source=" + source + "&repo=" + repo + "&by=" + by +
		"&from=${__from:date:seconds}&to=${__to:date:seconds}"
}

func infinityTimeseries(title, desc, source, repo, by string) *timeseries.PanelBuilder {
	return timeseries.NewPanelBuilder().
		Title(title).Description(desc).Unit(units.Short).
		Datasource(infinityDS()).Span(12).Height(8).FillOpacity(10).
		Legend(legend(true)).
		WithTarget(infinityTarget("A", tsURL(source, repo, by)))
}

// buildDataDashboard assembles the downloads-over-time dashboard. Build()
// schema-validates, so a malformed panel fails the generating derivation.
//
//nolint:staticcheck // v1 model required for portable file-based provisioning
func buildDataDashboard() (dashboard.Dashboard, error) {
	return dashboard.NewDashboardBuilder("ghdl — downloads").
		Uid("ghdl-downloads").
		Tags([]string{"ghdl", "downloads", "generated"}).
		Refresh("1h").
		Time("now-90d", "now").
		Timezone(common.TimeZoneBrowser).
		WithVariable(dashboard.NewDatasourceVariableBuilder(infinityVar).
			Label("ghdl API").Type("yesoreyeram-infinity-datasource")).
		WithRow(dashboard.NewRowBuilder("GitHub release downloads")).
		WithPanel(infinityTimeseries("Downloads by arch",
			"Cumulative GitHub release-asset downloads, summed by CPU architecture.",
			"github_release", ghRepo, "arch")).
		WithPanel(infinityTimeseries("Downloads by OS",
			"Cumulative GitHub release-asset downloads, summed by operating system.",
			"github_release", ghRepo, "os")).
		WithPanel(infinityTimeseries("Downloads by packaging format",
			"Cumulative downloads by packaging: deb, rpm, tar.gz, zip or raw binary.",
			"github_release", ghRepo, "format")).
		WithPanel(infinityTimeseries("Downloads by release",
			"Cumulative downloads per release tag.",
			"github_release", ghRepo, "release")).
		WithRow(dashboard.NewRowBuilder("Container pulls")).
		WithPanel(infinityTimeseries("Docker Hub pulls (repo total)",
			"Docker Hub only exposes a repo-wide pull_count — no per-tag breakdown.",
			"dockerhub", dockerRepo, "none")).
		WithPanel(infinityTimeseries("GHCR pulls (repo total)",
			"Total GHCR container pulls, scraped from the package page.",
			"ghcr", ghRepo, "none")).
		WithPanel(infinityTimeseries("GHCR pulls by version",
			"Per-version GHCR container pulls, scraped from the versions page.",
			"ghcr", ghRepo, "release")).
		Build()
}

// buildServiceDashboard assembles the scrape-health dashboard.
//
//nolint:staticcheck // v1 model required for portable file-based provisioning
func buildServiceDashboard() (dashboard.Dashboard, error) {
	return dashboard.NewDashboardBuilder("ghdl — service").
		Uid("ghdl-service").
		Tags([]string{"ghdl", "service", "generated"}).
		Refresh("1m").
		Time("now-24h", "now").
		Timezone(common.TimeZoneBrowser).
		WithVariable(dashboard.NewDatasourceVariableBuilder(promVar).
			Label("Data source").Type("prometheus")).
		WithRow(dashboard.NewRowBuilder("Health")).
		WithPanel(promStat("Series tracked",
			"Number of distinct series (source × repo × release × asset) in the database.",
			units.Short, "max(ghdl_series_total)", "series").
			GraphMode(common.BigValueGraphModeNone)).
		WithPanel(promStat("GitHub rate limit remaining",
			"GitHub API budget left after the last collection. Should stay near 5000.",
			units.Short, "min(ghdl_github_rate_limit_remaining)", "remaining")).
		WithRow(dashboard.NewRowBuilder("Freshness & errors")).
		WithPanel(promTimeseries("Time since last successful scrape",
			"Age of the last success per source. The freshness alert fires past 36h.",
			units.Seconds, true,
			promQuery("time() - ghdl_last_success_timestamp", "{{source}}"))).
		WithPanel(promTimeseries("Scrape errors",
			"Collection failures per source (1h increase). Sustained non-zero means a source is broken.",
			units.Short, true,
			promQuery("sum by (source) (increase(ghdl_scrape_errors_total[1h]))", "{{source}}"))).
		WithPanel(promTimeseries("Exporter & probe up",
			"Whether Prometheus can scrape ghdl (up) and the black-box probe succeeds.",
			units.Short, true,
			promQuery(`up{job="ghdl"}`, "up"),
			promQuery(`probe_success{job="ghdl"}`, "probe"))).
		Build()
}
