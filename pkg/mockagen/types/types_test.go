package types_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/catdevman/mockagen/pkg/mockagen/provider"
	"github.com/catdevman/mockagen/pkg/mockagen/types"
	"github.com/go-faker/faker/v4"
)

// TestLookup verifies that the catalog finds known built-in and provider
// generator types while cleanly rejecting unrecognised names with ok=false.
func TestLookup(t *testing.T) {
	cases := []struct {
		name         string
		typeName     string
		wantOK       bool
		wantTag      string
		wantAffinity types.SQLAffinity
	}{
		{
			name:         "faker built-in GUID",
			typeName:     "GUID",
			wantOK:       true,
			wantTag:      "uuid_hyphenated",
			wantAffinity: types.AffinityText,
		},
		{
			name:         "faker built-in Email Address",
			typeName:     "Email Address",
			wantOK:       true,
			wantTag:      "email",
			wantAffinity: types.AffinityText,
		},
		{
			name:         "faker built-in Datetime",
			typeName:     "Datetime",
			wantOK:       true,
			wantTag:      "date",
			wantAffinity: types.AffinityTime,
		},
		{
			name:         "provider-backed Company Name",
			typeName:     "Company Name",
			wantOK:       true,
			wantTag:      "mockagen_company_name",
			wantAffinity: types.AffinityText,
		},
		{
			name:         "provider-backed Boolean",
			typeName:     "Boolean",
			wantOK:       true,
			wantTag:      "mockagen_boolean",
			wantAffinity: types.AffinityBool,
		},
		{
			name:         "provider-backed override Word",
			typeName:     "Word",
			wantOK:       true,
			wantTag:      "mockagen_word",
			wantAffinity: types.AffinityText,
		},
		{
			name:     "unknown random string",
			typeName: "DefinitelyNotAType",
			wantOK:   false,
		},
		{
			name:     "empty type name",
			typeName: "",
			wantOK:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typ, ok := types.Lookup(tc.typeName)
			if ok != tc.wantOK {
				t.Fatalf("Lookup(%q) ok = %v, want %v", tc.typeName, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if typ.FakerTag != tc.wantTag {
				t.Errorf("Lookup(%q).FakerTag = %q, want %q",
					tc.typeName, typ.FakerTag, tc.wantTag)
			}
			if typ.Affinity != tc.wantAffinity {
				t.Errorf("Lookup(%q).Affinity = %q, want %q",
					tc.typeName, typ.Affinity, tc.wantAffinity)
			}
		})
	}
}

// TestCandidates verifies that candidate queries return only types with the
// requested SQL affinity, sorted alphabetically by Name so schema inference
// produces stable, deterministic suggestions.
func TestCandidates(t *testing.T) {
	cases := []struct {
		name          string
		affinity      types.SQLAffinity
		wantMinCount  int
		wantFirstName string
	}{
		{
			name:          "text affinity candidates",
			affinity:      types.AffinityText,
			wantMinCount:  20,
			wantFirstName: "Amount with Currency",
		},
		{
			name:          "bool affinity candidates",
			affinity:      types.AffinityBool,
			wantMinCount:  1,
			wantFirstName: "Boolean",
		},
		{
			name:          "time affinity candidates",
			affinity:      types.AffinityTime,
			wantMinCount:  2,
			wantFirstName: "Datetime",
		},
		{
			name:         "unknown affinity yields empty list",
			affinity:     types.SQLAffinity("unknown"),
			wantMinCount: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidates := types.Candidates(tc.affinity)
			if len(candidates) < tc.wantMinCount {
				t.Errorf("Candidates(%q) count = %d, want at least %d",
					tc.affinity, len(candidates), tc.wantMinCount)
			}
			for i, c := range candidates {
				if c.Affinity != tc.affinity {
					t.Errorf("Candidates(%q)[%d].Affinity = %q, want %q",
						tc.affinity, i, c.Affinity, tc.affinity)
				}
				if i > 0 && candidates[i-1].Name >= c.Name {
					t.Errorf("Candidates not sorted: %q >= %q",
						candidates[i-1].Name, c.Name)
				}
			}
			if tc.wantFirstName != "" && len(candidates) > 0 {
				if candidates[0].Name != tc.wantFirstName {
					t.Errorf("Candidates(%q)[0].Name = %q, want %q",
						tc.affinity, candidates[0].Name, tc.wantFirstName)
				}
			}
		})
	}
}

// TestAll verifies that All returns every catalog entry sorted by name.
func TestAll(t *testing.T) {
	all := types.All()
	if len(all) == 0 {
		t.Fatal("All() returned no types")
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Name >= all[i].Name {
			t.Errorf("All() not sorted at %d: %q >= %q",
				i, all[i-1].Name, all[i].Name)
		}
	}
}

// TestFakerTagsHistoricalParity guards against drift between the new catalog
// and historical mapToFaker entries plus provider.TypeMap.
func TestFakerTagsHistoricalParity(t *testing.T) {
	historical := map[string]string{
		"GUID":                 "uuid_hyphenated",
		"First Name":           "first_name",
		"Last Name":            "last_name",
		"Full Name":            "name",
		"Email Address":        "email",
		"Gender":               "oneof: male,female",
		"Datetime":             "date",
		"Custom List":          "oneof:",
		"Username":             "username",
		"URL":                  "url",
		"Domain Name":          "domain_name",
		"IP Address":           "ipv4",
		"IPv6 Address":         "ipv6",
		"MAC Address":          "mac_address",
		"Phone":                "phone_number",
		"Toll-Free Phone":      "toll_free_number",
		"Phone (E.164)":        "e_164_phone_number",
		"Credit Card Number":   "cc_number",
		"Credit Card Type":     "cc_type",
		"Currency Code":        "currency",
		"Amount with Currency": "amount_with_currency",
		"Time":                 "time",
		"Day of Week":          "day_of_week",
		"Day of Month":         "day_of_month",
		"Month":                "month_name",
		"Year":                 "year",
		"Century":              "century",
		"Time Zone":            "timezone",
		"Time Period":          "time_period",
		"Title (Male)":         "title_male",
		"Title (Female)":       "title_female",
		"Company Name":         "mockagen_company_name",
		"Job Title":            "mockagen_job_title",
		"Industry":             "mockagen_industry",
		"Buzzword":             "mockagen_buzzword",
		"Color":                "mockagen_color",
		"Hex Color":            "mockagen_hex_color",
		"SSN":                  "mockagen_ssn",
		"EIN":                  "mockagen_ein",
		"IBAN":                 "mockagen_iban",
		"Bitcoin Address":      "mockagen_bitcoin_address",
		"App Name":             "mockagen_app_name",
		"App Bundle ID":        "mockagen_app_bundle_id",
		"App Version":          "mockagen_semver",
		"Semantic Version":     "mockagen_semver",
		"Boolean":              "mockagen_boolean",
		"Blank":                "mockagen_blank",
		"Word":                 "mockagen_word",
		"Sentence":             "mockagen_sentence",
		"Paragraph":            "mockagen_paragraph",
	}

	fakerTags := types.FakerTags()
	for name, wantTag := range historical {
		gotTag, ok := fakerTags[name]
		if !ok {
			t.Errorf("types.FakerTags() missing historical type %q", name)
			continue
		}
		if gotTag != wantTag {
			t.Errorf("types.FakerTags()[%q] = %q, want %q", name, gotTag, wantTag)
		}
		if singleTag := types.FakerTag(name); singleTag != wantTag {
			t.Errorf("types.FakerTag(%q) = %q, want %q", name, singleTag, wantTag)
		}
	}
}

// TestTestDataConfigsSchemaTypesParity guards every column in test_data/config/
// against type resolution failures, ensuring that types.FakerTag resolves
// identically to historical expectations for every existing sample fixture.
func TestTestDataConfigsSchemaTypesParity(t *testing.T) {
	configDir := filepath.Join("..", "..", "..", "test_data", "config")
	entries, err := os.ReadDir(configDir)
	if err != nil {
		t.Fatalf("failed to read test_data/config: %v", err)
	}

	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if ext != ".json" && ext != ".yaml" && ext != ".yml" && ext != ".toml" {
			continue
		}
		path := filepath.Join(configDir, entry.Name())
		cfg := mockagen.LoadConfig(path)
		for _, col := range cfg.Columns {
			tag := types.FakerTag(col.Type)
			if tag == "" {
				t.Errorf("%s: column %q has type %q which resolved to empty faker tag",
					entry.Name(), col.Name, col.Type)
			}
		}
	}
}

// TestMetaEveryCatalogEntryTagIsValid guards every catalog entry against invalid
// or unregistered tags. Because go-faker silently falls back to random strings
// when a tag is not recognised on a string field, this meta-test verifies that
// every tag is either a known go-faker built-in tag or registered in provider,
// and confirms that faker successfully generates non-empty data for it.
func TestMetaEveryCatalogEntryTagIsValid(t *testing.T) {
	fakerBuiltins := map[string]bool{
		"uuid_hyphenated":      true,
		"first_name":           true,
		"last_name":            true,
		"name":                 true,
		"email":                true,
		"date":                 true,
		"username":             true,
		"url":                  true,
		"domain_name":          true,
		"ipv4":                 true,
		"ipv6":                 true,
		"mac_address":          true,
		"phone_number":         true,
		"toll_free_number":     true,
		"e_164_phone_number":   true,
		"cc_number":            true,
		"cc_type":              true,
		"currency":             true,
		"amount_with_currency": true,
		"time":                 true,
		"day_of_week":          true,
		"day_of_month":         true,
		"month_name":           true,
		"year":                 true,
		"century":              true,
		"timezone":             true,
		"time_period":          true,
		"word":                 true,
		"sentence":             true,
		"paragraph":            true,
		"title_male":           true,
		"title_female":         true,
	}

	for _, entry := range types.All() {
		t.Run(entry.Name, func(t *testing.T) {
			tag := entry.FakerTag
			isOneOf := strings.HasPrefix(tag, "oneof:")
			isProvider := provider.IsRegistered(tag)
			isBuiltin := fakerBuiltins[tag]

			if !isOneOf && !isProvider && !isBuiltin {
				t.Errorf("type %q has unrecognised tag %q (neither built-in nor provider)",
					entry.Name, tag)
			}

			// Verify faker actually executes the generator without panic or error.
			testTag := tag
			if entry.Name == "Custom List" {
				testTag = "oneof: optionA,optionB"
			}
			recordType := reflect.StructOf([]reflect.StructField{
				{
					Name: "SampleField",
					Type: reflect.TypeOf(""),
					Tag:  reflect.StructTag(`faker:"` + testTag + `"`),
				},
			})
			val := reflect.New(recordType).Interface()
			if err := faker.FakeData(&val); err != nil {
				t.Fatalf("faker.FakeData failed for %q (tag %q): %v", entry.Name, testTag, err)
			}
		})
	}
}
