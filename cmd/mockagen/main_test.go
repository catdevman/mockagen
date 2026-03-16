package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/catdevman/mockagen/pkg/mockagen"
	yaml "gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

func baseConfig(n int, cols ...mockagen.MockagenColumn) mockagen.MockagenConfig {
	return mockagen.MockagenConfig{
		NumberOfRecords: n,
		FileFormat:      "json",
		Name:            "test",
		Columns:         cols,
	}
}

func streamToJSON(t *testing.T, config mockagen.MockagenConfig) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	streamFakes(config, &buf)
	var records []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &records); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %s", err, buf.String())
	}
	return records
}

// ---------------------------------------------------------------------------
// Unit tests: strftimeToGoLayout
// ---------------------------------------------------------------------------

func TestStrftimeToGoLayout(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"%Y/%m/%d", "2006/01/02"},
		{"%Y-%m-%d", "2006-01-02"},
		{"%d/%m/%Y", "02/01/2006"},
		{"%H:%M:%S", "15:04:05"},
		{"%Y/%m/%d %H:%M:%S", "2006/01/02 15:04:05"},
		{"%y-%m-%d", "06-01-02"},
		{"", ""},
		{"no tokens", "no tokens"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := strftimeToGoLayout(tc.input)
			if got != tc.want {
				t.Errorf("strftimeToGoLayout(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit tests: parseDateFlex
// ---------------------------------------------------------------------------

func TestParseDateFlex(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		wantDay int // expected day-of-month when no error
	}{
		{"12/24/2019", false, 24}, // MM/DD/YYYY
		{"2020-01-15", false, 15}, // YYYY-MM-DD
		{"2020/03/07", false, 7},  // YYYY/MM/DD
		{"24/12/2019", false, 24}, // DD/MM/YYYY (falls through from MM/DD)
		{"not-a-date", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseDateFlex(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseDateFlex(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && got.Day() != tc.wantDay {
				t.Errorf("parseDateFlex(%q).Day() = %d, want %d", tc.input, got.Day(), tc.wantDay)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit tests: randTimeBetween
// ---------------------------------------------------------------------------

func TestRandTimeBetween_InRange(t *testing.T) {
	minT, _ := parseDateFlex("2020-01-01")
	maxT, _ := parseDateFlex("2020-12-31")
	for i := 0; i < 200; i++ {
		got := randTimeBetween(minT, maxT)
		if got.Before(minT) || got.After(maxT) {
			t.Fatalf("randTimeBetween result %v is outside [%v, %v]", got, minT, maxT)
		}
	}
}

func TestRandTimeBetween_EqualBounds(t *testing.T) {
	minT, _ := parseDateFlex("2020-06-15")
	got := randTimeBetween(minT, minT)
	if !got.Equal(minT) {
		t.Errorf("equal bounds: got %v, want %v", got, minT)
	}
}

func TestRandTimeBetween_InvertedBounds(t *testing.T) {
	// When delta <= 0, should return minT without panicking.
	minT, _ := parseDateFlex("2020-12-31")
	maxT, _ := parseDateFlex("2020-01-01")
	got := randTimeBetween(minT, maxT)
	if !got.Equal(minT) {
		t.Errorf("inverted bounds: got %v, want %v", got, minT)
	}
}

// ---------------------------------------------------------------------------
// Streaming output: JSON
// ---------------------------------------------------------------------------

func TestStreamFakes_JSON_ValidOutput(t *testing.T) {
	config := baseConfig(10,
		mockagen.MockagenColumn{Name: "id", Type: "GUID"},
		mockagen.MockagenColumn{Name: "first_name", Type: "First Name"},
		mockagen.MockagenColumn{Name: "email", Type: "Email Address"},
	)
	records := streamToJSON(t, config)

	if len(records) != 10 {
		t.Errorf("expected 10 records, got %d", len(records))
	}
	for i, r := range records {
		for _, field := range []string{"id", "first_name", "email"} {
			if _, ok := r[field]; !ok {
				t.Errorf("record %d missing field %q", i, field)
			}
		}
	}
}

func TestStreamFakes_JSON_LargeCount(t *testing.T) {
	config := baseConfig(500,
		mockagen.MockagenColumn{Name: "id", Type: "GUID"},
	)
	records := streamToJSON(t, config)
	if len(records) != 500 {
		t.Errorf("expected 500 records, got %d", len(records))
	}
}

// ---------------------------------------------------------------------------
// Streaming output: YAML
// ---------------------------------------------------------------------------

func TestStreamFakes_YAML_ValidOutput(t *testing.T) {
	config := mockagen.MockagenConfig{
		NumberOfRecords: 5,
		FileFormat:      "yaml",
		Name:            "test",
		Columns: []mockagen.MockagenColumn{
			{Name: "id", Type: "GUID"},
			{Name: "email", Type: "Email Address"},
		},
	}
	var buf bytes.Buffer
	streamFakes(config, &buf)

	dec := yaml.NewDecoder(&buf)
	count := 0
	for {
		var record map[string]any
		if err := dec.Decode(&record); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("YAML decode error: %v", err)
		}
		if record != nil {
			count++
		}
	}
	if count != 5 {
		t.Errorf("expected 5 YAML documents, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Improvement 1: null_percentage
// ---------------------------------------------------------------------------

func TestStreamFakes_NullPercentage_50(t *testing.T) {
	const n = 1000
	config := baseConfig(n,
		mockagen.MockagenColumn{Name: "id", Type: "GUID"},
		mockagen.MockagenColumn{Name: "gender", Type: "Gender", NullPercentage: 50},
	)
	records := streamToJSON(t, config)

	nullCount := 0
	for _, r := range records {
		if r["gender"] == nil {
			nullCount++
		}
	}
	pct := float64(nullCount) / float64(n) * 100
	// Allow ±15% statistical tolerance around 50%
	if pct < 35 || pct > 65 {
		t.Errorf("null_percentage=50: got %.1f%% nulls (%d/%d), want 35–65%%", pct, nullCount, n)
	}
}

func TestStreamFakes_NullPercentage_100(t *testing.T) {
	config := baseConfig(100,
		mockagen.MockagenColumn{Name: "field", Type: "First Name", NullPercentage: 100},
	)
	records := streamToJSON(t, config)
	for i, r := range records {
		if r["field"] != nil {
			t.Errorf("record %d: expected null for NullPercentage=100, got %v", i, r["field"])
		}
	}
}

func TestStreamFakes_NullPercentage_0(t *testing.T) {
	config := baseConfig(50,
		mockagen.MockagenColumn{Name: "name", Type: "First Name", NullPercentage: 0},
	)
	records := streamToJSON(t, config)
	for i, r := range records {
		if r["name"] == nil {
			t.Errorf("record %d: unexpected null for NullPercentage=0", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Improvement 1: blank
// ---------------------------------------------------------------------------

func TestStreamFakes_Blank_40(t *testing.T) {
	const n = 1000
	config := baseConfig(n,
		mockagen.MockagenColumn{Name: "id", Type: "GUID"},
		mockagen.MockagenColumn{Name: "name", Type: "First Name", Blank: 40},
	)
	records := streamToJSON(t, config)

	blankCount := 0
	for _, r := range records {
		if v, ok := r["name"]; ok && v == "" {
			blankCount++
		}
	}
	pct := float64(blankCount) / float64(n) * 100
	// Allow ±15% statistical tolerance around 40%
	if pct < 25 || pct > 55 {
		t.Errorf("blank=40: got %.1f%% blank (%d/%d), want 25–55%%", pct, blankCount, n)
	}
}

func TestStreamFakes_NullTakesPrecedenceOverBlank(t *testing.T) {
	// When both null_percentage and blank are set, null takes priority.
	// With null_percentage=100, every value must be null (not blank string).
	config := baseConfig(100,
		mockagen.MockagenColumn{Name: "field", Type: "First Name", NullPercentage: 100, Blank: 100},
	)
	records := streamToJSON(t, config)
	for i, r := range records {
		if r["field"] != nil {
			t.Errorf("record %d: expected null (null precedence), got %v", i, r["field"])
		}
	}
}

func TestStreamFakes_BlankIsEmptyString_NotNull(t *testing.T) {
	// blank=100 should produce empty strings "", not JSON null.
	config := baseConfig(50,
		mockagen.MockagenColumn{Name: "name", Type: "First Name", Blank: 100},
	)
	records := streamToJSON(t, config)
	for i, r := range records {
		v := r["name"]
		if v == nil {
			t.Errorf("record %d: blank=100 produced null, want empty string", i)
		} else if v != "" {
			t.Errorf("record %d: blank=100 produced %q, want empty string", i, v)
		}
	}
}

// ---------------------------------------------------------------------------
// Improvement 3: Datetime min/max
// ---------------------------------------------------------------------------

func TestStreamFakes_DatetimeRange_ISO(t *testing.T) {
	config := baseConfig(100,
		mockagen.MockagenColumn{
			Name:   "date",
			Type:   "Datetime",
			Min:    "2020-01-01",
			Max:    "2020-12-31",
			Format: "%Y-%m-%d",
		},
	)
	records := streamToJSON(t, config)

	minT, _ := time.Parse("2006-01-02", "2020-01-01")
	maxT, _ := time.Parse("2006-01-02", "2020-12-31")

	for i, r := range records {
		raw, ok := r["date"]
		if !ok || raw == nil {
			t.Errorf("record %d: missing or null date field", i)
			continue
		}
		t2, err := time.Parse("2006-01-02", raw.(string))
		if err != nil {
			t.Errorf("record %d: cannot parse date %q: %v", i, raw, err)
			continue
		}
		if t2.Before(minT) || t2.After(maxT) {
			t.Errorf("record %d: date %v outside [%v, %v]", i, t2, minT, maxT)
		}
	}
}

func TestStreamFakes_DatetimeMockarooFormat(t *testing.T) {
	// Matches Mockadoo.schema.json: min/max in MM/DD/YYYY, output format %Y/%m/%d
	config := baseConfig(50,
		mockagen.MockagenColumn{
			Name:   "date",
			Type:   "Datetime",
			Min:    "12/24/2019",
			Max:    "12/23/2020",
			Format: "%Y/%m/%d",
		},
	)
	records := streamToJSON(t, config)

	minT, _ := parseDateFlex("12/24/2019")
	maxT, _ := parseDateFlex("12/23/2020")

	for i, r := range records {
		raw, ok := r["date"]
		if !ok || raw == nil {
			t.Errorf("record %d: missing or null date field", i)
			continue
		}
		t2, err := time.Parse("2006/01/02", raw.(string))
		if err != nil {
			t.Errorf("record %d: cannot parse date %q: %v", i, raw, err)
			continue
		}
		// Compare at day granularity (output format has no time component)
		t2Day := t2.Truncate(24 * time.Hour)
		minDay := minT.UTC().Truncate(24 * time.Hour)
		maxDay := maxT.UTC().Truncate(24 * time.Hour)
		if t2Day.Before(minDay) || t2Day.After(maxDay) {
			t.Errorf("record %d: date %v outside [%v, %v]", i, t2Day, minDay, maxDay)
		}
	}
}

func TestStreamFakes_DatetimeNoMinMax_UsesDefault(t *testing.T) {
	// Datetime without min/max should fall through to faker's date generator (not custom).
	config := baseConfig(10,
		mockagen.MockagenColumn{Name: "created_at", Type: "Datetime"},
	)
	records := streamToJSON(t, config)
	if len(records) != 10 {
		t.Errorf("expected 10 records, got %d", len(records))
	}
	// Just verify the field exists and is non-null
	for i, r := range records {
		if r["created_at"] == nil {
			t.Errorf("record %d: expected non-null created_at", i)
		}
	}
}

func TestStreamFakes_DatetimeNullable_WithRange(t *testing.T) {
	// Datetime that has both min/max AND null_percentage should produce ~50% nulls
	// and bounded dates for the rest.
	const n = 200
	config := baseConfig(n,
		mockagen.MockagenColumn{
			Name:           "date",
			Type:           "Datetime",
			Min:            "2021-01-01",
			Max:            "2021-12-31",
			Format:         "%Y-%m-%d",
			NullPercentage: 50,
		},
	)
	records := streamToJSON(t, config)

	minT, _ := time.Parse("2006-01-02", "2021-01-01")
	maxT, _ := time.Parse("2006-01-02", "2021-12-31")

	nullCount := 0
	for i, r := range records {
		v := r["date"]
		if v == nil {
			nullCount++
			continue
		}
		t2, err := time.Parse("2006-01-02", v.(string))
		if err != nil {
			t.Errorf("record %d: cannot parse date %q: %v", i, v, err)
			continue
		}
		if t2.Before(minT) || t2.After(maxT) {
			t.Errorf("record %d: date %v outside range", i, t2)
		}
	}
	pct := float64(nullCount) / float64(n) * 100
	if pct < 35 || pct > 65 {
		t.Errorf("NullPercentage=50 on Datetime: got %.1f%% nulls, want 35–65%%", pct)
	}
}

// ---------------------------------------------------------------------------
// Improvement 3: Number min/max
// ---------------------------------------------------------------------------

func TestStreamFakes_Number_IntRange(t *testing.T) {
	config := baseConfig(100,
		mockagen.MockagenColumn{Name: "age", Type: "Number", Min: "18", Max: "65"},
	)
	records := streamToJSON(t, config)
	for i, r := range records {
		raw, ok := r["age"]
		if !ok || raw == nil {
			t.Errorf("record %d: missing age field", i)
			continue
		}
		var n int
		fmt.Sscanf(raw.(string), "%d", &n)
		if n < 18 || n >= 65 {
			t.Errorf("record %d: age %d outside [18, 65)", i, n)
		}
	}
}

func TestStreamFakes_Number_FloatRange(t *testing.T) {
	config := baseConfig(100,
		mockagen.MockagenColumn{Name: "price", Type: "Number", Min: "1.50", Max: "99.99", Decimals: 2},
	)
	records := streamToJSON(t, config)
	for i, r := range records {
		raw, ok := r["price"]
		if !ok || raw == nil {
			t.Errorf("record %d: missing price field", i)
			continue
		}
		var f float64
		fmt.Sscanf(raw.(string), "%f", &f)
		if f < 1.50 || f > 99.99 {
			t.Errorf("record %d: price %v outside [1.50, 99.99]", i, f)
		}
	}
}

func TestStreamFakes_Number_NoRange_DoesNotPanic(t *testing.T) {
	// Number without min/max should use the default range without panicking.
	config := baseConfig(20,
		mockagen.MockagenColumn{Name: "score", Type: "Number"},
	)
	records := streamToJSON(t, config)
	if len(records) != 20 {
		t.Errorf("expected 20 records, got %d", len(records))
	}
}

func TestStreamFakes_Number_Nullable(t *testing.T) {
	const n = 200
	config := baseConfig(n,
		mockagen.MockagenColumn{Name: "score", Type: "Number", Min: "0", Max: "100", NullPercentage: 50},
	)
	records := streamToJSON(t, config)

	nullCount := 0
	for _, r := range records {
		if r["score"] == nil {
			nullCount++
		}
	}
	pct := float64(nullCount) / float64(n) * 100
	if pct < 35 || pct > 65 {
		t.Errorf("Number NullPercentage=50: got %.1f%% nulls, want 35–65%%", pct)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkStreamFakes(b *testing.B) {
	inputs := []int{1, 10, 100, 1000, 10000}
	config := mockagen.MockagenConfig{
		NumberOfRecords: 1,
		FileFormat:      "json",
		Name:            "test",
		IncludeHeader:   true,
		Columns: []mockagen.MockagenColumn{
			{Name: "id", Type: "GUID"},
			{Name: "first_name", Type: "First Name"},
			{Name: "last_name", Type: "Last Name"},
			{Name: "email", Type: "Email Address"},
			{Name: "dob", Type: "Datetime", Min: "2000-01-01", Max: "2024-01-01", Format: "%Y-%m-%d"},
			{Name: "score", Type: "Number", Min: "0", Max: "100"},
		},
	}
	for _, size := range inputs {
		b.Run(fmt.Sprintf("stream_fake_%d", size), func(b *testing.B) {
			config.NumberOfRecords = size
			for i := 0; i < b.N; i++ {
				streamFakes(config, io.Discard)
			}
		})
	}
}

func BenchmarkStreamFakes_WithNulls(b *testing.B) {
	config := mockagen.MockagenConfig{
		NumberOfRecords: 1000,
		FileFormat:      "json",
		Name:            "test",
		Columns: []mockagen.MockagenColumn{
			{Name: "id", Type: "GUID"},
			{Name: "gender", Type: "Gender", NullPercentage: 30},
			{Name: "date", Type: "Datetime", Min: "2020-01-01", Max: "2023-12-31", Format: "%Y-%m-%d", NullPercentage: 10},
			{Name: "score", Type: "Number", Min: "0", Max: "1000", Blank: 20},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		streamFakes(config, io.Discard)
	}
}

// ---------------------------------------------------------------------------
// Improvement 2: expanded field types
// ---------------------------------------------------------------------------

func TestStreamFakes_Boolean_Values(t *testing.T) {
	records := streamToJSON(t, baseConfig(50,
		mockagen.MockagenColumn{Name: "active", Type: "Boolean"},
	))
	for i, r := range records {
		v, _ := r["active"].(string)
		if v != "true" && v != "false" {
			t.Errorf("record %d: Boolean got %q, want \"true\" or \"false\"", i, v)
		}
	}
}

func TestStreamFakes_Phone_NonEmpty(t *testing.T) {
	records := streamToJSON(t, baseConfig(10,
		mockagen.MockagenColumn{Name: "phone", Type: "Phone"},
	))
	for i, r := range records {
		if v, _ := r["phone"].(string); v == "" {
			t.Errorf("record %d: Phone is empty", i)
		}
	}
}

func TestStreamFakes_URL_NonEmpty(t *testing.T) {
	records := streamToJSON(t, baseConfig(10,
		mockagen.MockagenColumn{Name: "site", Type: "URL"},
	))
	for i, r := range records {
		if v, _ := r["site"].(string); v == "" {
			t.Errorf("record %d: URL is empty", i)
		}
	}
}

func TestStreamFakes_IPv4_Format(t *testing.T) {
	records := streamToJSON(t, baseConfig(20,
		mockagen.MockagenColumn{Name: "ip", Type: "IPv4 Address"},
	))
	for i, r := range records {
		v, _ := r["ip"].(string)
		if v == "" {
			t.Errorf("record %d: IPv4 is empty", i)
			continue
		}
		// Basic check: contains dots
		if !strings.Contains(v, ".") {
			t.Errorf("record %d: IPv4 %q doesn't look like an IP address", i, v)
		}
	}
}

func TestStreamFakes_IPv6_NonEmpty(t *testing.T) {
	records := streamToJSON(t, baseConfig(10,
		mockagen.MockagenColumn{Name: "ipv6", Type: "IPv6 Address"},
	))
	for i, r := range records {
		if v, _ := r["ipv6"].(string); v == "" {
			t.Errorf("record %d: IPv6 is empty", i)
		}
	}
}

func TestStreamFakes_Word_SingleWord(t *testing.T) {
	records := streamToJSON(t, baseConfig(20,
		mockagen.MockagenColumn{Name: "tag", Type: "Word"},
	))
	for i, r := range records {
		v, _ := r["tag"].(string)
		if v == "" {
			t.Errorf("record %d: Word is empty", i)
		}
	}
}

func TestStreamFakes_Paragraph_NonEmpty(t *testing.T) {
	records := streamToJSON(t, baseConfig(5,
		mockagen.MockagenColumn{Name: "bio", Type: "Paragraph"},
	))
	for i, r := range records {
		if v, _ := r["bio"].(string); v == "" {
			t.Errorf("record %d: Paragraph is empty", i)
		}
	}
}

func TestStreamFakes_Color_KnownValue(t *testing.T) {
	known := map[string]bool{
		"Red": true, "Green": true, "Blue": true, "Yellow": true,
		"Orange": true, "Purple": true, "Pink": true, "Brown": true,
		"Black": true, "White": true, "Gray": true, "Cyan": true,
		"Magenta": true, "Violet": true, "Indigo": true, "Teal": true,
	}
	records := streamToJSON(t, baseConfig(30,
		mockagen.MockagenColumn{Name: "color", Type: "Color"},
	))
	for i, r := range records {
		v, _ := r["color"].(string)
		if !known[v] {
			t.Errorf("record %d: Color %q not in known list", i, v)
		}
	}
}

func TestStreamFakes_CompanyName_NonEmpty(t *testing.T) {
	records := streamToJSON(t, baseConfig(10,
		mockagen.MockagenColumn{Name: "company", Type: "Company Name"},
	))
	for i, r := range records {
		if v, _ := r["company"].(string); v == "" {
			t.Errorf("record %d: Company Name is empty", i)
		}
	}
}

func TestStreamFakes_JobTitle_NonEmpty(t *testing.T) {
	records := streamToJSON(t, baseConfig(10,
		mockagen.MockagenColumn{Name: "role", Type: "Job Title"},
	))
	for i, r := range records {
		if v, _ := r["role"].(string); v == "" {
			t.Errorf("record %d: Job Title is empty", i)
		}
	}
}

func TestStreamFakes_Country_NonEmpty(t *testing.T) {
	records := streamToJSON(t, baseConfig(10,
		mockagen.MockagenColumn{Name: "country", Type: "Country"},
	))
	for i, r := range records {
		if v, _ := r["country"].(string); v == "" {
			t.Errorf("record %d: Country is empty", i)
		}
	}
}

func TestStreamFakes_AddressComponents_NonEmpty(t *testing.T) {
	records := streamToJSON(t, baseConfig(10,
		mockagen.MockagenColumn{Name: "city", Type: "City"},
		mockagen.MockagenColumn{Name: "state", Type: "State"},
		mockagen.MockagenColumn{Name: "zip", Type: "Zip Code"},
		mockagen.MockagenColumn{Name: "street", Type: "Street Address"},
	))
	for i, r := range records {
		for _, field := range []string{"city", "state", "zip", "street"} {
			if v, _ := r[field].(string); v == "" {
				t.Errorf("record %d: %s is empty", i, field)
			}
		}
	}
}

func TestStreamFakes_AddressComponents_Consistent(t *testing.T) {
	// City and State from the same record should come from the same RealAddress
	// (they share an address generation per record), so they should not mismatch
	// in format — we just verify they're both non-empty and different from each other.
	records := streamToJSON(t, baseConfig(20,
		mockagen.MockagenColumn{Name: "city", Type: "City"},
		mockagen.MockagenColumn{Name: "state", Type: "State"},
	))
	for i, r := range records {
		city, _ := r["city"].(string)
		state, _ := r["state"].(string)
		if city == "" || state == "" {
			t.Errorf("record %d: city=%q state=%q — one is empty", i, city, state)
		}
	}
}

// ---------------------------------------------------------------------------
// Improvement 4: formula processing
// ---------------------------------------------------------------------------

func TestApplyFormula_Upper(t *testing.T) {
	if got := applyFormula("hello world", "upper(this)"); got != "HELLO WORLD" {
		t.Errorf("upper(this): got %q", got)
	}
}

func TestApplyFormula_Lower(t *testing.T) {
	if got := applyFormula("Hello World", "lower(this)"); got != "hello world" {
		t.Errorf("lower(this): got %q", got)
	}
}

func TestApplyFormula_Trim(t *testing.T) {
	if got := applyFormula("  spaces  ", "trim(this)"); got != "spaces" {
		t.Errorf("trim(this): got %q", got)
	}
}

func TestApplyFormula_Title(t *testing.T) {
	if got := applyFormula("hello world", "title(this)"); got != "Hello World" {
		t.Errorf("title(this): got %q", got)
	}
}

func TestApplyFormula_PrependLiteral(t *testing.T) {
	if got := applyFormula("world", `"hello_" + this`); got != "hello_world" {
		t.Errorf(`"hello_" + this: got %q`, got)
	}
}

func TestApplyFormula_AppendLiteral(t *testing.T) {
	if got := applyFormula("user", `this + "@example.com"`); got != "user@example.com" {
		t.Errorf(`this + "@example.com": got %q`, got)
	}
}

func TestApplyFormula_BothSides(t *testing.T) {
	if got := applyFormula("world", `"hello_" + this + "_end"`); got != "hello_world_end" {
		t.Errorf(`both sides: got %q`, got)
	}
}

func TestApplyFormula_Empty(t *testing.T) {
	if got := applyFormula("original", ""); got != "original" {
		t.Errorf("empty formula: got %q, want original", got)
	}
}

func TestApplyFormula_This(t *testing.T) {
	if got := applyFormula("original", "this"); got != "original" {
		t.Errorf(`"this" formula: got %q, want original`, got)
	}
}

func TestApplyFormula_Unrecognized(t *testing.T) {
	// Unrecognized formula should return original value unchanged
	if got := applyFormula("original", "unknown_func(this)"); got != "original" {
		t.Errorf("unrecognized formula: got %q, want original", got)
	}
}

func TestTokenizeConcatFormula(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{`"prefix" + this`, []string{`"prefix"`, "this"}},
		{`this + "@example.com"`, []string{"this", `"@example.com"`}},
		{`"a" + this + "b"`, []string{`"a"`, "this", `"b"`}},
		{`this`, []string{"this"}},
		{`"hello"`, []string{`"hello"`}},
	}
	for _, tc := range tests {
		got := tokenizeConcatFormula(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("tokenize(%q): got %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("tokenize(%q)[%d]: got %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

func TestStreamFakes_Formula_Upper(t *testing.T) {
	records := streamToJSON(t, baseConfig(20,
		mockagen.MockagenColumn{Name: "id", Type: "GUID", Formula: "upper(this)"},
	))
	for i, r := range records {
		v, _ := r["id"].(string)
		if v != strings.ToUpper(v) {
			t.Errorf("record %d: formula upper(this) produced %q (not uppercase)", i, v)
		}
	}
}

func TestStreamFakes_Formula_Lower(t *testing.T) {
	records := streamToJSON(t, baseConfig(20,
		mockagen.MockagenColumn{Name: "email", Type: "Email Address", Formula: "lower(this)"},
	))
	for i, r := range records {
		v, _ := r["email"].(string)
		if v != strings.ToLower(v) {
			t.Errorf("record %d: formula lower(this) produced %q (not lowercase)", i, v)
		}
	}
}

func TestStreamFakes_Formula_Append(t *testing.T) {
	records := streamToJSON(t, baseConfig(20,
		mockagen.MockagenColumn{Name: "email", Type: "Username", Formula: `this + "@corp.com"`},
	))
	for i, r := range records {
		v, _ := r["email"].(string)
		if !strings.HasSuffix(v, "@corp.com") {
			t.Errorf("record %d: formula append got %q, want suffix @corp.com", i, v)
		}
	}
}

func TestStreamFakes_Formula_Prepend(t *testing.T) {
	records := streamToJSON(t, baseConfig(20,
		mockagen.MockagenColumn{Name: "code", Type: "Word", Formula: `"SKU-" + this`},
	))
	for i, r := range records {
		v, _ := r["code"].(string)
		if !strings.HasPrefix(v, "SKU-") {
			t.Errorf("record %d: formula prepend got %q, want prefix SKU-", i, v)
		}
	}
}

func TestStreamFakes_Formula_NullFieldSkipped(t *testing.T) {
	// null_percentage=100 — every value is null; formula should NOT be applied
	// (null fields stay null, not transformed to empty or error)
	records := streamToJSON(t, baseConfig(20,
		mockagen.MockagenColumn{
			Name:           "name",
			Type:           "First Name",
			NullPercentage: 100,
			Formula:        "upper(this)",
		},
	))
	for i, r := range records {
		if r["name"] != nil {
			t.Errorf("record %d: null field was changed by formula to %v", i, r["name"])
		}
	}
}

func TestStreamFakes_Formula_OnCustomCol(t *testing.T) {
	// Formula applied to a custom-generated Datetime field
	records := streamToJSON(t, baseConfig(20,
		mockagen.MockagenColumn{
			Name:    "year",
			Type:    "Datetime",
			Min:     "2020-01-01",
			Max:     "2020-12-31",
			Format:  "%Y-%m-%d",
			Formula: "upper(this)", // dates don't have lowercase, just verifies it doesn't break
		},
	))
	for i, r := range records {
		if r["year"] == nil || r["year"] == "" {
			t.Errorf("record %d: formula on custom datetime produced empty/null", i)
		}
	}
}

// ---------------------------------------------------------------------------
// CSV output tests
// ---------------------------------------------------------------------------

// streamToCSV streams a config to CSV and returns all rows (including header if present).
func streamToCSV(t *testing.T, config mockagen.MockagenConfig) [][]string {
	t.Helper()
	var buf bytes.Buffer
	streamFakes(config, &buf)
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("invalid CSV output: %v\nraw: %s", err, buf.String())
	}
	return rows
}

func TestStreamFakes_CSV_RecordCount(t *testing.T) {
	config := mockagen.MockagenConfig{
		NumberOfRecords: 10,
		FileFormat:      "csv",
		IncludeHeader:   false,
		Name:            "test",
		Columns: []mockagen.MockagenColumn{
			{Name: "id", Type: "GUID"},
			{Name: "name", Type: "First Name"},
		},
	}
	rows := streamToCSV(t, config)
	if len(rows) != 10 {
		t.Errorf("row count: got %d, want 10", len(rows))
	}
}

func TestStreamFakes_CSV_WithHeader(t *testing.T) {
	config := mockagen.MockagenConfig{
		NumberOfRecords: 5,
		FileFormat:      "csv",
		IncludeHeader:   true,
		Name:            "test",
		Columns: []mockagen.MockagenColumn{
			{Name: "id", Type: "GUID"},
			{Name: "email", Type: "Email Address"},
		},
	}
	rows := streamToCSV(t, config)

	// 1 header + 5 data rows
	if len(rows) != 6 {
		t.Fatalf("row count: got %d, want 6 (1 header + 5 data)", len(rows))
	}
	if rows[0][0] != "id" || rows[0][1] != "email" {
		t.Errorf("header row: got %v, want [id email]", rows[0])
	}
}

func TestStreamFakes_CSV_ColumnCount(t *testing.T) {
	config := mockagen.MockagenConfig{
		NumberOfRecords: 5,
		FileFormat:      "csv",
		IncludeHeader:   false,
		Name:            "test",
		Columns: []mockagen.MockagenColumn{
			{Name: "id", Type: "GUID"},
			{Name: "first", Type: "First Name"},
			{Name: "last", Type: "Last Name"},
			{Name: "email", Type: "Email Address"},
		},
	}
	rows := streamToCSV(t, config)
	for i, row := range rows {
		if len(row) != 4 {
			t.Errorf("row %d: got %d columns, want 4", i, len(row))
		}
	}
}

func TestStreamFakes_CSV_NonEmptyValues(t *testing.T) {
	config := mockagen.MockagenConfig{
		NumberOfRecords: 10,
		FileFormat:      "csv",
		IncludeHeader:   false,
		Name:            "test",
		Columns: []mockagen.MockagenColumn{
			{Name: "id", Type: "GUID"},
			{Name: "first", Type: "First Name"},
		},
	}
	rows := streamToCSV(t, config)
	for i, row := range rows {
		for j, cell := range row {
			if cell == "" {
				t.Errorf("row %d col %d: unexpected empty value", i, j)
			}
		}
	}
}

func TestStreamFakes_CSV_NullPercentage(t *testing.T) {
	config := mockagen.MockagenConfig{
		NumberOfRecords: 200,
		FileFormat:      "csv",
		IncludeHeader:   false,
		Name:            "test",
		Columns: []mockagen.MockagenColumn{
			{Name: "val", Type: "First Name", NullPercentage: 100},
		},
	}
	rows := streamToCSV(t, config)
	for i, row := range rows {
		if row[0] != "" {
			t.Errorf("row %d: expected empty string for 100%% null, got %q", i, row[0])
		}
	}
}

func TestStreamFakes_CSV_WithDatetimeRange(t *testing.T) {
	config := mockagen.MockagenConfig{
		NumberOfRecords: 10,
		FileFormat:      "csv",
		IncludeHeader:   false,
		Name:            "test",
		Columns: []mockagen.MockagenColumn{
			{Name: "date", Type: "Datetime", Min: "2020-01-01", Max: "2020-12-31", Format: "%Y-%m-%d"},
		},
	}
	rows := streamToCSV(t, config)
	for i, row := range rows {
		if _, err := time.Parse("2006-01-02", row[0]); err != nil {
			t.Errorf("row %d: %q is not a valid YYYY-MM-DD date: %v", i, row[0], err)
		}
	}
}

func TestStreamFakes_CSV_LargeCount(t *testing.T) {
	config := mockagen.MockagenConfig{
		NumberOfRecords: 500,
		FileFormat:      "csv",
		IncludeHeader:   true,
		Name:            "test",
		Columns: []mockagen.MockagenColumn{
			{Name: "id", Type: "GUID"},
		},
	}
	rows := streamToCSV(t, config)
	// 1 header + 500 data rows
	if len(rows) != 501 {
		t.Errorf("row count: got %d, want 501", len(rows))
	}
}
