package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catdevman/mockagen/pkg/mockagen"
)

func TestFluentWriterLineShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newFluentWriter(f, "mockagen.test")
	type rec struct {
		User string `json:"user"`
		IP   string `json:"ip"`
	}
	for _, r := range []rec{{"alice", "10.0.0.1"}, {"bob", "10.0.0.2"}} {
		if err := w.WriteRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), b)
	}
	for i, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Fatalf("line %d: got %d tab-separated fields, want 3: %q", i, len(parts), line)
		}
		if _, err := time.Parse(time.RFC3339, parts[0]); err != nil {
			t.Errorf("line %d: timestamp %q: %v", i, parts[0], err)
		}
		if parts[1] != "mockagen.test" {
			t.Errorf("line %d: tag = %q, want %q", i, parts[1], "mockagen.test")
		}
		var got rec
		if err := json.Unmarshal([]byte(parts[2]), &got); err != nil {
			t.Errorf("line %d: record %q: %v", i, parts[2], err)
		}
	}
}

func TestFluentTag(t *testing.T) {
	cases := []struct {
		name   string
		config mockagen.MockagenConfig
		want   string
	}{
		{"explicit tag wins", mockagen.MockagenConfig{Name: "web logs", Tag: "app.access"}, "app.access"},
		{"blank tag falls back to name", mockagen.MockagenConfig{Name: "web logs", Tag: "  "}, "mockagen.web.logs"},
		{"derived from name", mockagen.MockagenConfig{Name: "Web Logs"}, "mockagen.web.logs"},
		{"no name at all", mockagen.MockagenConfig{}, "mockagen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fluentTag(tc.config); got != tc.want {
				t.Errorf("fluentTag() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutputExt(t *testing.T) {
	for format, want := range map[string]string{
		"json":    "json",
		"yaml":    "yaml",
		"fixed":   "fixed",
		"parquet": "parquet",
		"fluent":  "log",
	} {
		if got := outputExt(format); got != want {
			t.Errorf("outputExt(%q) = %q, want %q", format, got, want)
		}
	}
}
