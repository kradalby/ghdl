package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
)

func main() {
	out := flag.String("out", "", "directory to write ghdl.json and ghdl-service.json (default: print ghdl.json to stdout)")
	flag.Parse()

	data, err := buildDataDashboard()
	check(err, "build data dashboard")
	service, err := buildServiceDashboard()
	check(err, "build service dashboard")

	if *out == "" {
		emit(os.Stdout, data)
		return
	}

	writeFile(filepath.Join(*out, "ghdl.json"), data)
	writeFile(filepath.Join(*out, "ghdl-service.json"), service)
}

func writeFile(path string, d dashboard.Dashboard) {
	f, err := os.Create(path)
	check(err, "create "+path)
	defer f.Close()
	emit(f, d)
}

func emit(w *os.File, d dashboard.Dashboard) {
	b, err := json.MarshalIndent(d, "", "  ")
	check(err, "marshal dashboard")
	_, err = w.Write(append(b, '\n'))
	check(err, "write dashboard")
}

func check(err error, what string) {
	if err != nil {
		fmt.Fprintln(os.Stderr, what+":", err)
		os.Exit(1)
	}
}
