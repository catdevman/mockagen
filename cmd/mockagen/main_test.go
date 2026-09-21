package main

import (
	"fmt"
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
