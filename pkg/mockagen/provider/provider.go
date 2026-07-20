// Package provider registers go-faker tags for Mockaroo field types that
// go-faker has no built-in generator for. Only parameter-free types live
// here: faker.AddProvider registers a func(reflect.Value) (any, error) with
// no way to receive per-column config (min/max, a regex pattern, weights,
// etc.), so anything needing that has to be handled outside this mechanism.
package provider

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"

	"github.com/go-faker/faker/v4"
)

// TypeMap maps Mockaroo schema type names to the faker tags this package
// registers in init(). Callers merge this into their own type->tag mapping
// the same way built-in go-faker tags are used.
var TypeMap = map[string]string{
	"Company Name":     "mockagen_company_name",
	"Job Title":        "mockagen_job_title",
	"Industry":         "mockagen_industry",
	"Buzzword":         "mockagen_buzzword",
	"Color":            "mockagen_color",
	"Hex Color":        "mockagen_hex_color",
	"SSN":              "mockagen_ssn",
	"EIN":              "mockagen_ein",
	"IBAN":             "mockagen_iban",
	"Bitcoin Address":  "mockagen_bitcoin_address",
	"App Name":         "mockagen_app_name",
	"App Bundle ID":    "mockagen_app_bundle_id",
	"App Version":      "mockagen_semver",
	"Semantic Version": "mockagen_semver",
	"Boolean":          "mockagen_boolean",
	"Blank":            "mockagen_blank",
}

func init() {
	register("mockagen_company_name", companyName)
	register("mockagen_job_title", jobTitle)
	register("mockagen_industry", industry)
	register("mockagen_buzzword", buzzword)
	register("mockagen_color", colorName)
	register("mockagen_hex_color", hexColor)
	register("mockagen_ssn", ssn)
	register("mockagen_ein", ein)
	register("mockagen_iban", iban)
	register("mockagen_bitcoin_address", bitcoinAddress)
	register("mockagen_app_name", appName)
	register("mockagen_app_bundle_id", appBundleID)
	register("mockagen_semver", semanticVersion)
	register("mockagen_boolean", boolean)
	register("mockagen_blank", blank)
}

// register wires a zero-argument string generator up to a faker tag.
func register(tag string, gen func() string) {
	if err := faker.AddProvider(tag, func(_ reflect.Value) (any, error) {
		return gen(), nil
	}); err != nil {
		panic(fmt.Sprintf("provider: failed to register tag %q: %v", tag, err))
	}
}

func pick(list []string) string {
	return list[rand.IntN(len(list))]
}

var companyAdjectives = []string{
	"Global", "Dynamic", "Innovative", "Advanced", "Strategic", "Premier",
	"Unified", "Integrated", "Apex", "Core", "Prime", "Vertex", "Summit",
	"Pioneer", "Nexus",
}
var companyNouns = []string{
	"Solutions", "Systems", "Industries", "Technologies", "Ventures",
	"Dynamics", "Enterprises", "Holdings", "Partners", "Networks",
	"Innovations", "Group", "Labs",
}
var companySuffixes = []string{"Inc", "LLC", "Group", "Co", "Corp", "Ltd"}

func companyName() string {
	return fmt.Sprintf("%s %s %s", pick(companyAdjectives), pick(companyNouns), pick(companySuffixes))
}

var jobLevels = []string{
	"Senior", "Lead", "Chief", "Junior", "Regional", "Global", "Principal",
	"Executive", "Associate", "Staff",
}
var jobDepartments = []string{
	"Marketing", "Sales", "Engineering", "Product", "Operations", "Finance",
	"Data", "Design", "Support", "Research",
}
var jobRoles = []string{
	"Manager", "Director", "Analyst", "Engineer", "Specialist", "Coordinator",
	"Consultant", "Officer", "Architect", "Strategist",
}

func jobTitle() string {
	return fmt.Sprintf("%s %s %s", pick(jobLevels), pick(jobDepartments), pick(jobRoles))
}

var industries = []string{
	"Healthcare", "Technology", "Finance", "Retail", "Manufacturing",
	"Education", "Real Estate", "Transportation", "Hospitality",
	"Agriculture", "Telecommunications", "Energy", "Construction",
	"Entertainment", "Insurance", "Automotive", "Pharmaceuticals",
	"Logistics", "Aerospace", "Biotechnology",
}

func industry() string {
	return pick(industries)
}

var buzzVerbs = []string{
	"Leverage", "Synergize", "Optimize", "Streamline", "Empower",
	"Transform", "Accelerate", "Maximize", "Cultivate", "Orchestrate",
}
var buzzAdjectives = []string{
	"scalable", "innovative", "next-generation", "cross-platform",
	"cutting-edge", "enterprise", "holistic", "dynamic", "seamless", "robust",
}
var buzzNouns = []string{
	"synergies", "platforms", "solutions", "ecosystems", "paradigms",
	"deliverables", "infrastructures", "metrics", "channels", "frameworks",
}

func buzzword() string {
	return fmt.Sprintf("%s %s %s", pick(buzzVerbs), pick(buzzAdjectives), pick(buzzNouns))
}

var colorNames = []string{
	"Red", "Blue", "Green", "Yellow", "Orange", "Purple", "Turquoise",
	"Crimson", "Maroon", "Azure", "Indigo", "Violet", "Magenta", "Teal",
	"Coral", "Gold", "Silver", "Charcoal", "Ivory", "Lavender", "Olive",
	"Navy", "Beige", "Salmon", "Mint",
}

func colorName() string {
	return pick(colorNames)
}

func hexColor() string {
	return fmt.Sprintf("#%06X", rand.IntN(1<<24))
}

// ssn avoids the 000/666/900-999 area number ranges the SSA never issues,
// so generated values read as plausible without landing on a documented
// invalid or reserved range.
func ssn() string {
	area := rand.IntN(899) + 1
	if area == 666 {
		area++
	}
	group := rand.IntN(99) + 1
	serial := rand.IntN(9999) + 1
	return fmt.Sprintf("%03d-%02d-%04d", area, group, serial)
}

func ein() string {
	return fmt.Sprintf("%02d-%07d", rand.IntN(100), rand.IntN(10000000))
}

var ibanCountries = []string{"GB", "DE", "FR", "ES", "IT", "NL", "BE", "CH", "SE", "NO"}

const ibanAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// iban produces a format-plausible IBAN (country code + check digits +
// alphanumeric BBAN). It does not compute a real mod-97 checksum.
func iban() string {
	var bban strings.Builder
	for range 18 {
		bban.WriteByte(ibanAlphabet[rand.IntN(len(ibanAlphabet))])
	}
	return fmt.Sprintf("%s%02d%s", pick(ibanCountries), rand.IntN(100), bban.String())
}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// bitcoinAddress produces a legacy-format-looking address (leading '1' plus
// base58 characters). It is not a real, spendable address.
func bitcoinAddress() string {
	length := 25 + rand.IntN(10)
	var b strings.Builder
	b.WriteByte('1')
	for i := 1; i < length; i++ {
		b.WriteByte(base58Alphabet[rand.IntN(len(base58Alphabet))])
	}
	return b.String()
}

var appAdjectives = []string{"Swift", "Pixel", "Cloud", "Nimbus", "Quick", "Bright", "Smart", "Flux", "Spark", "Vivid"}
var appNouns = []string{"Note", "Flow", "Sync", "Track", "Board", "Hub", "Loop", "Path", "Base", "Wave"}

func appName() string {
	return pick(appAdjectives) + pick(appNouns)
}

var bundleWords = []string{"acme", "globex", "initech", "umbrella", "stark", "wayne", "hooli", "piedpiper", "soylent", "wonka"}

func appBundleID() string {
	return fmt.Sprintf("com.%s.%s", pick(bundleWords), strings.ToLower(appName()))
}

func semanticVersion() string {
	return fmt.Sprintf("%d.%d.%d", rand.IntN(10), rand.IntN(20), rand.IntN(20))
}

func boolean() string {
	if rand.IntN(2) == 0 {
		return "false"
	}
	return "true"
}

func blank() string {
	return ""
}
