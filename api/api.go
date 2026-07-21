// Package api serves ghdl's stored history as JSON for Grafana's Infinity
// datasource. Two endpoints: /api/series lists the tracked series (dashboard
// variables), /api/timeseries returns aggregated step-function download counts.
package api

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kradalby/ghdl/db"
)

// Server answers the JSON API against a ghdl database.
type Server struct {
	db  *db.DB
	now func() time.Time
}

// New builds an API server.
func New(d *db.DB) *Server {
	return &Server{db: d, now: time.Now}
}

// SeriesResponse is one row of /api/series.
type SeriesResponse struct {
	ID      int64  `json:"id"`
	Source  string `json:"source"`
	Repo    string `json:"repo"`
	Release string `json:"release"`
	Asset   string `json:"asset"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Format  string `json:"format"`
}

// Series handles GET /api/series?source=&repo=.
func (s *Server) Series(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Series(r.Context(), r.URL.Query().Get("source"), r.URL.Query().Get("repo"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := make([]SeriesResponse, 0, len(rows))
	for _, s := range rows {
		out = append(out, SeriesResponse{
			ID: s.ID, Source: s.Source, Repo: s.Repo, Release: s.Release,
			Asset: s.Asset, OS: s.Os, Arch: s.Arch, Format: s.Format,
		})
	}

	writeJSON(w, r, out)
}

// TimeSeries handles GET /api/timeseries?source=&repo=&by=&from=&to=.
func (s *Server) TimeSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := parseTime(q.Get("from"), 0)
	to := parseTime(q.Get("to"), s.now().Unix())

	points, err := s.db.TimeSeries(r.Context(), q.Get("source"), q.Get("repo"), q.Get("by"), from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if points == nil {
		points = []db.Point{}
	}

	writeJSON(w, r, points)
}

// parseTime reads a unix timestamp, tolerating Grafana's millisecond epochs
// (values past ~year 33658 in seconds are treated as milliseconds).
func parseTime(s string, def int64) int64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	if v > 1_000_000_000_000 {
		v /= 1000
	}
	return v
}

// writeJSON encodes v, gzipping when the client accepts it (the series/point
// payloads compress well and are read over the tailnet).
func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")

	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		_ = json.NewEncoder(gz).Encode(v)
		return
	}

	_ = json.NewEncoder(w).Encode(v)
}
