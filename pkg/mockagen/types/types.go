// Package types maintains the catalog of data generator types known to
// mockagen. It combines Mockaroo-compatible type names with go-faker struct
// tags, database SQL affinities, parameter constraints, and naming hints
// used by schema inference.
package types

import (
	"slices"
	"strings"

	"github.com/catdevman/mockagen/pkg/mockagen/provider"
)

// SQLAffinity classifies generator types into coarse database affinities.
// Used by schema inference to select candidate types for a given SQL column
// type without hardcoding parallel lookup tables.
type SQLAffinity string

const (
	AffinityText   SQLAffinity = "text"
	AffinityInt    SQLAffinity = "int"
	AffinityFloat  SQLAffinity = "float"
	AffinityBool   SQLAffinity = "bool"
	AffinityTime   SQLAffinity = "time"
	AffinityBinary SQLAffinity = "binary"
)

// Type represents a generator type registered in mockagen's catalog.
// It bundles the faker struct tag with metadata, affinity, parameter
// requirements, and inference name hints.
type Type struct {
	Name           string
	FakerTag       string
	Kind           string
	Affinity       SQLAffinity
	RequiresValues bool
	RequiresMin    bool
	RequiresMax    bool
	RequiresFormat bool
	NameHints      []string
}

// catalog holds every registered Type indexed by its Mockaroo type name.
// Initialised with faker built-ins and populated with provider-backed types
// at init time.
var catalog = map[string]Type{
	"GUID": {
		Name:      "GUID",
		FakerTag:  "uuid_hyphenated",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"id", "guid", "uuid"},
	},
	"First Name": {
		Name:      "First Name",
		FakerTag:  "first_name",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"first_name", "fname", "given_name"},
	},
	"Last Name": {
		Name:      "Last Name",
		FakerTag:  "last_name",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"last_name", "lname", "surname"},
	},
	"Full Name": {
		Name:      "Full Name",
		FakerTag:  "name",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"full_name", "name"},
	},
	"Email Address": {
		Name:      "Email Address",
		FakerTag:  "email",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"email", "email_address", "*_email"},
	},
	"Gender": {
		Name:      "Gender",
		FakerTag:  "oneof: male,female",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"gender", "sex"},
	},
	"Datetime": {
		Name:      "Datetime",
		FakerTag:  "date",
		Kind:      "string",
		Affinity:  AffinityTime,
		NameHints: []string{"created_at", "updated_at", "*_at", "datetime", "timestamp"},
	},
	"Custom List": {
		Name:           "Custom List",
		FakerTag:       "oneof:",
		Kind:           "string",
		Affinity:       AffinityText,
		RequiresValues: true,
		NameHints:      nil,
	},
	"Username": {
		Name:      "Username",
		FakerTag:  "username",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"username", "user_name", "login"},
	},
	"URL": {
		Name:      "URL",
		FakerTag:  "url",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"url", "website", "link"},
	},
	"Domain Name": {
		Name:      "Domain Name",
		FakerTag:  "domain_name",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"domain", "domain_name", "host"},
	},
	"IP Address": {
		Name:      "IP Address",
		FakerTag:  "ipv4",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"ip", "ip_address", "ipv4"},
	},
	"IPv6 Address": {
		Name:      "IPv6 Address",
		FakerTag:  "ipv6",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"ipv6"},
	},
	"MAC Address": {
		Name:      "MAC Address",
		FakerTag:  "mac_address",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"mac", "mac_address"},
	},
	"Phone": {
		Name:      "Phone",
		FakerTag:  "phone_number",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"phone", "phone_number", "tel"},
	},
	"Toll-Free Phone": {
		Name:      "Toll-Free Phone",
		FakerTag:  "toll_free_number",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"toll_free", "toll_free_phone"},
	},
	"Phone (E.164)": {
		Name:      "Phone (E.164)",
		FakerTag:  "e_164_phone_number",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"e164", "phone_e164"},
	},
	"Credit Card Number": {
		Name:      "Credit Card Number",
		FakerTag:  "cc_number",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"credit_card", "cc_number", "card_number"},
	},
	"Credit Card Type": {
		Name:      "Credit Card Type",
		FakerTag:  "cc_type",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"credit_card_type", "cc_type", "card_type"},
	},
	"Currency Code": {
		Name:      "Currency Code",
		FakerTag:  "currency",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"currency", "currency_code"},
	},
	"Amount with Currency": {
		Name:      "Amount with Currency",
		FakerTag:  "amount_with_currency",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"amount_with_currency"},
	},
	"Time": {
		Name:      "Time",
		FakerTag:  "time",
		Kind:      "string",
		Affinity:  AffinityTime,
		NameHints: []string{"time"},
	},
	"Day of Week": {
		Name:      "Day of Week",
		FakerTag:  "day_of_week",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"day_of_week", "weekday"},
	},
	"Day of Month": {
		Name:      "Day of Month",
		FakerTag:  "day_of_month",
		Kind:      "string",
		Affinity:  AffinityInt,
		NameHints: []string{"day_of_month"},
	},
	"Month": {
		Name:      "Month",
		FakerTag:  "month_name",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"month", "month_name"},
	},
	"Year": {
		Name:      "Year",
		FakerTag:  "year",
		Kind:      "string",
		Affinity:  AffinityInt,
		NameHints: []string{"year"},
	},
	"Century": {
		Name:      "Century",
		FakerTag:  "century",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"century"},
	},
	"Time Zone": {
		Name:      "Time Zone",
		FakerTag:  "timezone",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"timezone", "tz"},
	},
	"Time Period": {
		Name:      "Time Period",
		FakerTag:  "time_period",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"time_period"},
	},
	"Title (Male)": {
		Name:      "Title (Male)",
		FakerTag:  "title_male",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"title_male"},
	},
	"Title (Female)": {
		Name:      "Title (Female)",
		FakerTag:  "title_female",
		Kind:      "string",
		Affinity:  AffinityText,
		NameHints: []string{"title_female"},
	},
}

// providerTypeMetadata provides SQL affinity and name hints for types backed
// by the provider package. Tags are omitted here and merged directly from
// provider.TypeMap at init time so provider stays the single source of truth.
var providerTypeMetadata = map[string]struct {
	affinity  SQLAffinity
	nameHints []string
}{
	"Company Name":     {AffinityText, []string{"company", "company_name", "org_name", "organization"}},
	"Job Title":        {AffinityText, []string{"job_title", "title", "role", "position"}},
	"Industry":         {AffinityText, []string{"industry", "sector"}},
	"Buzzword":         {AffinityText, []string{"buzzword"}},
	"Color":            {AffinityText, []string{"color", "colour"}},
	"Hex Color":        {AffinityText, []string{"hex_color", "color_hex"}},
	"SSN":              {AffinityText, []string{"ssn", "social_security_number"}},
	"EIN":              {AffinityText, []string{"ein", "employer_id"}},
	"IBAN":             {AffinityText, []string{"iban"}},
	"Bitcoin Address":  {AffinityText, []string{"bitcoin", "btc_address", "crypto_address"}},
	"App Name":         {AffinityText, []string{"app_name", "application"}},
	"App Bundle ID":    {AffinityText, []string{"bundle_id", "app_bundle_id", "package_name"}},
	"App Version":      {AffinityText, []string{"app_version", "version"}},
	"Semantic Version": {AffinityText, []string{"semver", "semantic_version"}},
	"Boolean":          {AffinityBool, []string{"is_*", "has_*", "active", "enabled", "boolean", "bool"}},
	"Blank":            {AffinityText, nil},
	"Word":             {AffinityText, []string{"word"}},
	"Sentence":         {AffinityText, []string{"sentence", "description", "comment", "note", "text"}},
	"Paragraph":        {AffinityText, []string{"paragraph", "body", "content", "bio"}},
}

// init merges provider.TypeMap into catalog without duplicating provider tags,
// ensuring provider.TypeMap remains the sole declaration of provider tags.
func init() {
	for name, meta := range providerTypeMetadata {
		tag, ok := provider.TypeMap[name]
		if !ok {
			continue
		}
		catalog[name] = Type{
			Name:      name,
			FakerTag:  tag,
			Kind:      "string",
			Affinity:  meta.affinity,
			NameHints: meta.nameHints,
		}
	}
	// Also register any extra provider tags added in the future.
	for name, tag := range provider.TypeMap {
		if _, exists := catalog[name]; !exists {
			catalog[name] = Type{
				Name:     name,
				FakerTag: tag,
				Kind:     "string",
				Affinity: AffinityText,
			}
		}
	}
}

// Lookup reports whether a generator type is registered in the catalog and
// returns its specification.
func Lookup(name string) (Type, bool) {
	t, ok := catalog[name]
	return t, ok
}

// Candidates returns all registered generator types matching the requested
// SQL affinity, sorted alphabetically by Name for stable output.
func Candidates(affinity SQLAffinity) []Type {
	var matches []Type
	for _, t := range catalog {
		if t.Affinity == affinity {
			matches = append(matches, t)
		}
	}
	slices.SortFunc(matches, func(a, b Type) int {
		return strings.Compare(a.Name, b.Name)
	})
	return matches
}

// FakerTag returns the struct tag string for type name, or an empty string
// if the type is unknown. Matches historical mapToFaker lookup behavior.
func FakerTag(name string) string {
	if t, ok := catalog[name]; ok {
		return t.FakerTag
	}
	return ""
}

// FakerTags returns a copy of the mapping from Mockaroo type names to faker
// struct tags. Provided for compatibility with legacy test helpers.
func FakerTags() map[string]string {
	out := make(map[string]string, len(catalog))
	for name, t := range catalog {
		out[name] = t.FakerTag
	}
	return out
}

// All returns a slice of all registered generator types in the catalog, sorted
// alphabetically by Name.
func All() []Type {
	out := make([]Type, 0, len(catalog))
	for _, t := range catalog {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Type) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}
