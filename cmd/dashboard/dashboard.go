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
		"parser":        "backend", // required, or Infinity returns the rows unparsed and the panel is empty
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

// filters is the drill-down selection pinned on a panel. Empty fields (the "All"
// variable value) match everything.
type filters struct {
	release, os, arch, format string
}

// tsURL builds a /api/timeseries URL with the pinned filters and Grafana's
// time-range macros. Params are emitted in a fixed order so the generated JSON
// is deterministic.
func tsURL(source, repo, by string, f filters) string {
	return "/api/timeseries?source=" + source + "&repo=" + repo + "&by=" + by +
		param("release", f.release) + param("os", f.os) + param("arch", f.arch) + param("format", f.format) +
		"&from=${__from:date:seconds}&to=${__to:date:seconds}"
}

// param renders a filter query param, or "" when the value is empty (All).
func param(key, val string) string {
	if val == "" {
		return ""
	}
	return "&" + key + "=" + val
}

func infinityTimeseries(title, desc, source, repo, by string, f filters) *timeseries.PanelBuilder {
	return timeseries.NewPanelBuilder().
		Title(title).Description(desc).Unit(units.Short).
		Datasource(infinityDS()).Span(12).Height(8).FillOpacity(10).
		Legend(legend(true)).
		WithTarget(infinityTarget("A", tsURL(source, repo, by, f)))
}

// valuesVar builds an "All"-able template variable populated from /api/values,
// so the viewer can pin a dimension and the panels filter by it.
func valuesVar(name, label, field, source, repo string) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder(name).
		Label(label).
		Datasource(infinityDS()).
		Query(dashboard.StringOrMap{Map: map[string]any{
			"refId":         "variable",
			"datasource":    map[string]any{"type": "yesoreyeram-infinity-datasource", "uid": "${" + infinityVar + "}"},
			"type":          "json",
			"source":        "url",
			"parser":        "backend", // required, or the dropdown stays empty
			"format":        "table",
			"url":           "/api/values?field=" + field + "&source=" + source + "&repo=" + repo,
			"url_options":   map[string]any{"method": "GET"},
			"root_selector": "",
			"columns":       []map[string]any{{"selector": "value", "text": "value", "type": "string"}},
		}}).
		Refresh(dashboard.VariableRefreshOnDashboardLoad).
		Sort(dashboard.VariableSortAlphabeticalDesc).
		IncludeAll(true).AllValue("").Multi(false)
}

// buildDataDashboard assembles the downloads-over-time dashboard with drill-down
// on version × packaging × arch. Build() schema-validates, so a malformed panel
// fails the generating derivation.
//
//nolint:staticcheck // v1 model required for portable file-based provisioning
func buildDataDashboard() (dashboard.Dashboard, error) {
	// The variable interpolations pinned on the filtered panels.
	v, a, fmtV := "${version}", "${arch}", "${format}"

	return dashboard.NewDashboardBuilder("ghdl — downloads").
		Uid("ghdl-downloads").
		Tags([]string{"ghdl", "downloads", "generated"}).
		Refresh("1h").
		Time("now-90d", "now").
		Timezone(common.TimeZoneBrowser).
		WithVariable(dashboard.NewDatasourceVariableBuilder(infinityVar).
			Label("ghdl API").Type("yesoreyeram-infinity-datasource")).
		WithVariable(valuesVar("version", "Version (release/tag)", "release", "github_release", ghRepo)).
		WithVariable(valuesVar("arch", "Arch", "arch", "github_release", ghRepo)).
		WithVariable(valuesVar("format", "Packaging", "format", "github_release", ghRepo)).

		// GitHub releases — each panel pins two dimensions (from the variables)
		// and splits by the third, so version × packaging × arch drill-down.
		WithRow(dashboard.NewRowBuilder("GitHub releases — drill down with the Version / Arch / Packaging variables")).
		WithPanel(infinityTimeseries("Downloads by arch",
			"GitHub release-asset downloads split by CPU arch, filtered by the pinned Version and Packaging.",
			"github_release", ghRepo, "arch", filters{release: v, format: fmtV})).
		WithPanel(infinityTimeseries("Downloads by packaging",
			"Split by packaging (deb/rpm/tar.gz/zip/bin), filtered by the pinned Version and Arch.",
			"github_release", ghRepo, "format", filters{release: v, arch: a})).
		WithPanel(infinityTimeseries("Downloads by version",
			"Split by release version, filtered by the pinned Arch and Packaging.",
			"github_release", ghRepo, "release", filters{arch: a, format: fmtV})).
		WithPanel(infinityTimeseries("Downloads by OS",
			"Split by operating system, filtered by the pinned Version.",
			"github_release", ghRepo, "os", filters{release: v})).

		// GHCR containers — per-version and per-arch (from the registry index).
		WithRow(dashboard.NewRowBuilder("GHCR containers")).
		WithPanel(infinityTimeseries("Pulls by arch",
			"Per-arch container pulls (sub-manifest downloads), filtered by the pinned Version.",
			"ghcr", ghRepo, "arch", filters{release: v})).
		WithPanel(infinityTimeseries("Pulls by version",
			"Per-version container pulls, filtered by the pinned Arch.",
			"ghcr", ghRepo, "release", filters{arch: a})).

		// Docker Hub — repo total only (the API exposes nothing finer).
		WithRow(dashboard.NewRowBuilder("Docker Hub (repo total only)")).
		WithPanel(infinityTimeseries("Docker Hub pulls",
			"Docker Hub exposes only a repo-wide pull_count — no per-tag or per-arch breakdown.",
			"dockerhub", dockerRepo, "none", filters{})).
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
			units.Short, "max(ghdl_series)", "series").
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
