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
	// These three override go-faker tags that do exist, rather than filling
	// a gap. Its sentence generator calls rand.Perm over the *entire*
	// 250-word list to choose 6 words, so every sentence costs 250 calls to
	// faker's mutex-guarded global RNG and a 250-int allocation, then runs
	// the first word through golang.org/x/text's title caser. A CPU profile
	// of generation put 39% of all samples in that one path. The versions
	// below index into the word list directly, through math/rand/v2's per-P
	// state, which takes no lock at all.
	"Word":      "mockagen_word",
	"Sentence":  "mockagen_sentence",
	"Paragraph": "mockagen_paragraph",
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
	register("mockagen_word", word)
	register("mockagen_sentence", sentence)
	register("mockagen_paragraph", paragraph)
}

// registered records every tag init() wired up, so a test can verify that
// TypeMap does not point at a tag nothing registers - a typo there would
// silently fall through to faker generating a plain random string.
var registered = map[string]bool{}

// register wires a zero-argument string generator up to a faker tag.
func register(tag string, gen func() string) {
	registered[tag] = true
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

// loremWords is the vocabulary the text generators draw from. Latin, to match
// what Mockaroo's Word/Sentence/Paragraph types produce.
var loremWords = []string{
	"a", "ab", "accusamus", "ad", "alias", "aliquam", "aliquid", "amet",
	"animi", "aperiam", "architecto", "asperiores", "aspernatur", "assumenda",
	"at", "atque", "aut", "autem", "beatae", "blanditiis", "commodi",
	"consectetur", "consequatur", "corporis", "corrupti", "culpa", "cum",
	"cupiditate", "debitis", "delectus", "deleniti", "deserunt", "dicta",
	"differt", "dignissimos", "distinctio", "dolor", "dolore", "dolorem",
	"doloremque", "dolores", "doloribus", "dolorum", "ducimus", "ea", "eaque",
	"earum", "eius", "eligendi", "enim", "eos", "error", "esse", "est", "et",
	"eum", "eveniet", "ex", "excepturi", "exercitationem", "expedita",
	"explicabo", "facere", "facilis", "fuga", "fugiat", "fugit", "harum",
	"hic", "id", "illo", "illum", "impedit", "in", "incidunt", "inventore",
	"ipsa", "ipsam", "ipsum", "iste", "itaque", "iure", "iusto", "labore",
	"laboriosam", "laborum", "laudantium", "libero", "magnam", "magni",
	"maiores", "maxime", "minima", "minus", "modi", "molestiae", "molestias",
	"mollitia", "nam", "natus", "necessitatibus", "nemo", "neque", "nesciunt",
	"nihil", "nisi", "nobis", "non", "nostrum", "nulla", "numquam", "occaecati",
	"odio", "odit", "officia", "officiis", "omnis", "optio", "pariatur",
	"perferendis", "perspiciatis", "placeat", "porro", "possimus", "praesentium",
	"provident", "quae", "quaerat", "quam", "quas", "quasi", "qui", "quia",
	"quibusdam", "quidem", "quis", "quo", "quod", "ratione", "recusandae",
	"reiciendis", "rem", "repellat", "repellendus", "reprehenderit",
	"repudiandae", "rerum", "saepe", "sapiente", "sed", "sequi", "similique",
	"sint", "sit", "soluta", "sunt", "suscipit", "tempora", "tempore",
	"temporibus", "tenetur", "totam", "ut", "vel", "velit", "veniam", "veritatis",
	"vero", "vitae", "voluptas", "voluptate", "voluptatem", "voluptates",
	"voluptatibus", "voluptatum",
}

func word() string {
	return pick(loremWords)
}

// sentenceInto appends one capitalised, full-stopped sentence to b. Callers
// that need several share one builder rather than joining strings.
func sentenceInto(b *strings.Builder) {
	words := 4 + rand.IntN(9)
	for i := range words {
		w := pick(loremWords)
		if i == 0 {
			// Every word in the list is lowercase ASCII, so upper-casing
			// the first byte is enough - no need for a Unicode title caser.
			b.WriteByte(w[0] - ('a' - 'A'))
			b.WriteString(w[1:])
			continue
		}
		b.WriteByte(' ')
		b.WriteString(w)
	}
	b.WriteByte('.')
}

func sentence() string {
	var b strings.Builder
	b.Grow(96)
	sentenceInto(&b)
	return b.String()
}

func paragraph() string {
	var b strings.Builder
	b.Grow(384)
	for i := range 3 + rand.IntN(3) {
		if i > 0 {
			b.WriteByte(' ')
		}
		sentenceInto(&b)
	}
	return b.String()
}
