package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/mockagen"
)

// fakesBenchConfig is the schema BenchmarkGenerateFakes sweeps over. The
// Type values are Mockaroo type names, which is what a real config carries
// and what mapToFaker is keyed on - not go-faker tag names. An unknown type
// is not an error anywhere: mapToFaker returns "", the field gets an empty
// faker tag, and faker fills it with a plain random string. The benchmark
// still runs, it just stops measuring the generators it names.
func fakesBenchConfig(rows int) mockagen.MockagenConfig {
	return mockagen.MockagenConfig{
		NumberOfRecords: rows,
		FileFormat:      "json",
		Name:            "test",
		IncludeHeader:   true,
		Columns: []mockagen.MockagenColumn{
			{Name: "id", Type: "GUID"},
			{Name: "username", Type: "Username"},
			{Name: "firstName", Type: "First Name"},
			{Name: "lastName", Type: "Last Name"},
			{Name: "dob", Type: "Datetime"},
		},
	}
}

func BenchmarkGenerateFakes(b *testing.B) {
	inputs := []int{1, 10, 100, 1000, 10000}
	config := fakesBenchConfig(1)
	for _, size := range inputs {
		b.Run(fmt.Sprintf("generate_fake_%d", size), func(b *testing.B) {
			config.NumberOfRecords = size
			for i := 0; i < b.N; i++ {
				// The channel must be drained. generateFakes returns as soon as
				// the workers are spawned, so leaving it unconsumed measured
				// only the setup cost while leaking a blocked worker pool per
				// iteration - which exhausts memory once b.N grows.
				fakesCh, _ := generateFakes(config)
				for range fakesCh {
				}
			}
		})
	}
}

// TestBenchmarkSchemaTypesAreKnown guards every benchmark schema against the
// silent fallback described on fakesBenchConfig: a type mapToFaker does not
// recognise produces a random string rather than an error, so a wrong or
// stale type name here is invisible until someone reads the generated data.
func TestBenchmarkSchemaTypesAreKnown(t *testing.T) {
	configs := map[string]mockagen.MockagenConfig{
		"fakesBenchConfig": fakesBenchConfig(1),
		"benchConfig":      benchConfig("json", 1),
	}
	for name, config := range configs {
		for _, col := range config.Columns {
			if _, ok := mapToFaker[col.Type]; !ok {
				t.Errorf("%s: column %q has type %q, which mapToFaker does not know",
					name, col.Name, col.Type)
			}
		}
	}
}

// TestOutputPath verifies that an explicit -output flag overrides the default
// location while preserving the historical ./output/<name>.<ext> convention
// when omitted.
func TestOutputPath(t *testing.T) {
	cases := []struct {
		name     string
		config   mockagen.MockagenConfig
		explicit string
		want     string
	}{
		{
			name:   "default with spaces converted to dashes",
			config: mockagen.MockagenConfig{Name: "web logs", FileFormat: "csv"},
			want:   "./output/web-logs.csv",
		},
		{
			name:   "default with fluent format mapping to log",
			config: mockagen.MockagenConfig{Name: "app events", FileFormat: "fluent"},
			want:   "./output/app-events.log",
		},
		{
			name:     "explicit flag overrides default path",
			config:   mockagen.MockagenConfig{Name: "web logs", FileFormat: "csv"},
			explicit: "/tmp/custom/data.csv",
			want:     "/tmp/custom/data.csv",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := outputPath(tc.config, tc.explicit); got != tc.want {
				t.Errorf("outputPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEnsureOutputDir guards against writer crashes caused by missing parent
// directories: running mockagen outside a directory with an existing ./output/
// folder must create the parent directory rather than failing on os.Create.
func TestEnsureOutputDir(t *testing.T) {
	t.Run("nested directory created when absent", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "deep", "nested", "out.csv")
		if err := ensureOutputDir(dest); err != nil {
			t.Fatalf("ensureOutputDir(%q): %v", dest, err)
		}
		info, err := os.Stat(filepath.Dir(dest))
		if err != nil {
			t.Fatalf("parent dir stat: %v", err)
		}
		if !info.IsDir() {
			t.Errorf("parent %q is not a directory", filepath.Dir(dest))
		}
	})

	t.Run("bare file in working directory", func(t *testing.T) {
		if err := ensureOutputDir("out.csv"); err != nil {
			t.Errorf("ensureOutputDir(\"out.csv\"): %v", err)
		}
	})
}

// TestGenerateWritesToConfiguredPath pins that a complete generation run
// writes directly to the explicit path requested, creating absent parent
// directories.
func TestGenerateWritesToConfiguredPath(t *testing.T) {
	out := filepath.Join(t.TempDir(), "sub", "dir", "records.csv")
	config := mockagen.MockagenConfig{
		NumberOfRecords: 2,
		FileFormat:      "csv",
		Name:            "records",
		IncludeHeader:   true,
		Columns: []mockagen.MockagenColumn{
			{Name: "id", Type: "GUID"},
		},
	}
	if err := ensureOutputDir(out); err != nil {
		t.Fatal(err)
	}
	fakesCh, structArr := generateFakes(config)
	w, err := newRecordWriter(config, structArr, out)
	if err != nil {
		t.Fatalf("newRecordWriter: %v", err)
	}
	for fake := range fakesCh {
		if err := w.WriteRecord(fake); err != nil {
			t.Fatalf("WriteRecord: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", out, err)
	}
	if len(b) == 0 {
		t.Errorf("expected generated CSV bytes in %q, got empty file", out)
	}
}

// TestBuildMetadataVariables asserts that version, commit, and date exist and
// carry dev placeholders when built without goreleaser's -X ldflags.
func TestBuildMetadataVariables(t *testing.T) {
	if version != "dev" {
		t.Errorf("version = %q, want %q", version, "dev")
	}
	if commit != "none" {
		t.Errorf("commit = %q, want %q", commit, "none")
	}
	if date != "unknown" {
		t.Errorf("date = %q, want %q", date, "unknown")
	}
}

// TestCLISubcommandDispatch tests the CLI router across all subcommands, legacy
// flags, and error conditions without requiring external processes.
func TestCLISubcommandDispatch(t *testing.T) {
	// Written fresh rather than pointing at test_data: the sample schemas
	// there ask for a million rows, and the five gen cases below generated
	// them once each, which was 21 of the unit suite's 22 seconds. These
	// cases exercise argument routing, so two rows prove as much as a
	// million. The same bytes are written twice to cover the .yml extension,
	// which LoadConfig accepts as of the kind loader.
	const genConfig = `num_rows: 2
file_format: yaml
name: dispatch
columns:
  - name: id
    type: GUID
`
	dir := t.TempDir()
	sampleCfg := filepath.Join(dir, "config.yaml")
	ymlPath := filepath.Join(dir, "config.yml")
	for _, p := range []string{sampleCfg, ymlPath} {
		if err := os.WriteFile(p, []byte(genConfig), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}

	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "bare mockagen exits non-zero and prints usage",
			args:       []string{},
			wantCode:   1,
			wantStderr: "Usage: mockagen <command>",
		},
		{
			name:       "unknown subcommand prints error and names valid verbs",
			args:       []string{"bogus"},
			wantCode:   1,
			wantStderr: "unknown subcommand \"bogus\". Valid subcommands: gen, schema, map, import, version, help",
		},
		{
			name:       "help subcommand prints usage to stdout",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: "Usage: mockagen <command>",
		},
		{
			name:       "version subcommand prints version metadata",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "mockagen version dev (commit: none, date: unknown)",
		},
		{
			name:       "legacy -version flag prints version metadata",
			args:       []string{"-version"},
			wantCode:   0,
			wantStdout: "mockagen version dev",
		},
		{
			name:       "gen without -config prints error and usage",
			args:       []string{"gen"},
			wantCode:   1,
			wantStderr: "-config is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(tc.args, &stdout, &stderr)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			if tc.wantStdout != "" && !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Errorf("stdout %q does not contain %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr %q does not contain %q", stderr.String(), tc.wantStderr)
			}
		})
	}

	genCases := []struct {
		name string
		args func(out string) []string
	}{
		{
			name: "legacy -config flag",
			args: func(out string) []string {
				return []string{"-config", sampleCfg, "-output", out}
			},
		},
		{
			name: "legacy --config flag",
			args: func(out string) []string {
				return []string{"--config", sampleCfg, "-output", out}
			},
		},
		{
			name: "legacy -config= flag",
			args: func(out string) []string {
				return []string{"-config=" + sampleCfg, "-output=" + out}
			},
		},
		{
			name: "subcommand gen -config",
			args: func(out string) []string {
				return []string{"gen", "-config", sampleCfg, "-output", out}
			},
		},
		{
			name: "subcommand gen with .yml config",
			args: func(out string) []string {
				return []string{"gen", "-config", ymlPath, "-output", out}
			},
		},
	}
	for _, tc := range genCases {
		t.Run(tc.name, func(t *testing.T) {
			outPath := filepath.Join(t.TempDir(), "out.yaml")
			var stdout, stderr bytes.Buffer
			args := tc.args(outPath)
			if code := runCLI(args, &stdout, &stderr); code != 0 {
				t.Fatalf("runCLI(%v) failed with code %d:\nstderr: %s", args, code, stderr.String())
			}
			info, err := os.Stat(outPath)
			if err != nil {
				t.Fatalf("output file stat: %v", err)
			}
			if info.Size() == 0 {
				t.Errorf("output file %q is empty", outPath)
			}
		})
	}
}
