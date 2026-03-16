package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/go-faker/faker/v4"
	fixed "github.com/ianlopshire/go-fixedwidth"
	yaml "gopkg.in/yaml.v3"
)

var configFile string

// mapToFaker maps Mockaroo column types to faker struct tag values.
// Types that require custom generation (Datetime+bounds, Number, address components)
// are handled separately in isCustomCol / applyCustomCols.
var mapToFaker = map[string]string{
	// Original types
	"GUID":          "uuid_hyphenated",
	"First Name":    "first_name",
	"Last Name":     "last_name",
	"Email Address": "email",
	"Gender":        "oneof: male,female",
	"Datetime":      "date",
	"Custom List":   "oneof:",
	// Improvement 2: expanded types
	"Boolean":      "oneof: true,false",
	"Phone":        "phone_number",
	"URL":          "url",
	"IPv4 Address": "ipv4",
	"IPv6 Address": "ipv6",
	"Username":     "username",
	"Domain Name":  "domain_name",
	"MAC Address":  "mac_address",
	"Word":         "word",
	"Sentence":     "sentence",
	"Paragraph":    "paragraph",
	// address components and multi-word lists handled via isCustomCol / applyCustomCols
	"Color": "oneof: Red,Green,Blue,Yellow,Orange,Purple,Pink,Brown,Black,White,Gray,Cyan,Magenta,Violet,Indigo,Teal",
}

// listValues holds curated value lists for types that need multi-word values
// but cannot use faker's oneof: tag (which does not support spaces in values).
var listValues = map[string][]string{
	"Country": {
		"United States", "Canada", "United Kingdom", "Germany", "France",
		"Australia", "Japan", "China", "Brazil", "India", "Mexico", "Italy",
		"Spain", "South Korea", "Netherlands", "Sweden", "Norway", "Switzerland",
		"New Zealand", "Argentina",
	},
	"Company Name": {
		"Acme Corp", "Globex Corp", "Initech", "Umbrella Inc", "Waystar Royco",
		"Weyland-Yutani", "Tyrell Corporation", "Dunder Mifflin", "Sterling Cooper",
		"Massive Dynamic", "Pied Piper", "Vandelay Industries", "Soylent Corp",
	},
	"Job Title": {
		"Software Engineer", "Product Manager", "Data Scientist", "UX Designer",
		"DevOps Engineer", "Marketing Manager", "Sales Executive", "CTO", "CEO",
		"COO", "VP of Engineering", "Director of Product", "QA Engineer", "Data Engineer",
	},
}

// ---------------------------------------------------------------------------
// Datetime helpers
// ---------------------------------------------------------------------------

// strftimeMap converts strftime tokens to Go time layout tokens.
var strftimeMap = []struct{ from, to string }{
	{"%Y", "2006"},
	{"%m", "01"},
	{"%d", "02"},
	{"%H", "15"},
	{"%M", "04"},
	{"%S", "05"},
	{"%y", "06"},
	{"%b", "Jan"},
	{"%B", "January"},
	{"%p", "PM"},
	{"%Z", "MST"},
}

// strftimeToGoLayout converts a strftime format string to a Go time layout string.
func strftimeToGoLayout(format string) string {
	result := format
	for _, r := range strftimeMap {
		result = strings.ReplaceAll(result, r.from, r.to)
	}
	return result
}

// parseDateFlex tries common date formats in order and returns the first successful parse.
// MM/DD/YYYY is tried first because Mockaroo exports min/max in that format.
func parseDateFlex(s string) (time.Time, error) {
	formats := []string{
		"01/02/2006", // MM/DD/YYYY - Mockaroo min/max default
		"2006/01/02", // YYYY/MM/DD
		"2006-01-02", // YYYY-MM-DD (ISO 8601)
		"02/01/2006", // DD/MM/YYYY
		"January 2, 2006",
		"Jan 2, 2006",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse date %q with any known format", s)
}

// randTimeBetween returns a uniformly random time.Time between minT and maxT (inclusive).
// Returns minT if minT >= maxT.
func randTimeBetween(minT, maxT time.Time) time.Time {
	delta := maxT.Unix() - minT.Unix()
	if delta <= 0 {
		return minT
	}
	return minT.Add(time.Duration(rand.Int63n(delta)) * time.Second)
}

// ---------------------------------------------------------------------------
// Formula helpers
// ---------------------------------------------------------------------------

// toTitleCase converts a string to title case (first letter of each word uppercase).
func toTitleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	return strings.Join(words, " ")
}

// tokenizeConcatFormula splits a formula string on + operators outside of quoted strings.
// e.g. `"prefix_" + this + "_suffix"` -> [`"prefix_"`, `this`, `"_suffix"`]
func tokenizeConcatFormula(f string) []string {
	var tokens []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)
	for i := 0; i < len(f); i++ {
		c := f[i]
		if inQuote {
			current.WriteByte(c)
			if c == quoteChar {
				inQuote = false
			}
		} else {
			switch c {
			case '"', '\'':
				inQuote = true
				quoteChar = c
				current.WriteByte(c)
			case '+':
				if tok := strings.TrimSpace(current.String()); tok != "" {
					tokens = append(tokens, tok)
				}
				current.Reset()
			default:
				current.WriteByte(c)
			}
		}
	}
	if tok := strings.TrimSpace(current.String()); tok != "" {
		tokens = append(tokens, tok)
	}
	return tokens
}

// applyFormula applies a Mockaroo-style formula to a string value.
// Supported: upper(this), lower(this), trim(this), title(this),
// and string concatenation using + with quoted literals and "this".
// Returns val unchanged if the formula is empty or unrecognized.
func applyFormula(val, formula string) string {
	f := strings.TrimSpace(formula)
	if f == "" || f == "this" {
		return val
	}
	switch f {
	case "upper(this)":
		return strings.ToUpper(val)
	case "lower(this)":
		return strings.ToLower(val)
	case "trim(this)":
		return strings.TrimSpace(val)
	case "title(this)":
		return toTitleCase(val)
	}
	// Concatenation expression
	tokens := tokenizeConcatFormula(f)
	if len(tokens) == 0 {
		return val
	}
	var sb strings.Builder
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "this" {
			sb.WriteString(val)
		} else if len(tok) >= 2 &&
			((tok[0] == '"' && tok[len(tok)-1] == '"') ||
				(tok[0] == '\'' && tok[len(tok)-1] == '\'')) {
			sb.WriteString(tok[1 : len(tok)-1])
		}
		// unrecognized tokens are skipped
	}
	if result := sb.String(); result != "" {
		return result
	}
	return val // fallback: unrecognized formula
}

// ---------------------------------------------------------------------------
// Column descriptors
// ---------------------------------------------------------------------------

// nullableCol describes a column that can produce null or blank values.
type nullableCol struct {
	index          int
	nullPercentage int64
	blank          int64
}

// customCol describes a column whose values are generated outside of faker
// (Datetime with min/max bounds, Number columns, address components, list types).
type customCol struct {
	index   int
	colType string
	// Datetime
	minTime  time.Time
	maxTime  time.Time
	goLayout string
	// Number
	minVal   float64
	maxVal   float64
	decimals int64
	isFloat  bool
	// Address component: "City", "State", "Zip Code", "Street Address"
	addressField string
	// List: randomly pick from a fixed slice (for multi-word values)
	listVals []string
	// whether the struct field is *string (nullable) or string
	isPointer bool
}

// formulaCol describes a column with a post-generation formula transformation.
type formulaCol struct {
	index   int
	formula string
}

// ---------------------------------------------------------------------------
// Column classification
// ---------------------------------------------------------------------------

// isCustomCol reports whether a column requires manual generation rather than faker.
func isCustomCol(col mockagen.MockagenColumn) bool {
	switch col.Type {
	case "Datetime":
		return col.Min != "" || col.Max != ""
	case "Number":
		return true
	case "City", "State", "Zip Code", "Street Address":
		return true
	}
	_, isList := listValues[col.Type]
	return isList
}

// ---------------------------------------------------------------------------
// Field mutation helpers
// ---------------------------------------------------------------------------

// setStringField sets a struct field of kind string or *string to val.
func setStringField(field reflect.Value, val string) {
	if field.Kind() == reflect.Ptr {
		v := val
		field.Set(reflect.ValueOf(&v))
	} else {
		field.SetString(val)
	}
}

// applyCustomCols fills fields tagged faker:"-" with manually generated values.
func applyCustomCols(rv reflect.Value, cols []customCol) {
	var addr faker.RealAddress
	addrLoaded := false

	for _, cc := range cols {
		field := rv.Field(cc.index)
		var val string
		switch cc.colType {
		case "Datetime":
			t := randTimeBetween(cc.minTime, cc.maxTime)
			val = t.Format(cc.goLayout)

		case "Number":
			if cc.isFloat {
				delta := cc.maxVal - cc.minVal
				if delta <= 0 {
					delta = 1000.0
				}
				v := rand.Float64()*delta + cc.minVal
				val = fmt.Sprintf("%.*f", cc.decimals, v)
			} else {
				delta := int64(cc.maxVal - cc.minVal)
				if delta <= 0 {
					delta = 1000000
				}
				val = strconv.FormatInt(rand.Int63n(delta)+int64(cc.minVal), 10)
			}

		case "address":
			if !addrLoaded {
				addr = faker.GetRealAddress()
				addrLoaded = true
			}
			switch cc.addressField {
			case "City":
				val = addr.City
			case "State":
				val = addr.State
			case "Zip Code":
				val = addr.PostalCode
			case "Street Address":
				val = addr.Address
			default:
				val = addr.City
			}

		case "list":
			val = cc.listVals[rand.Intn(len(cc.listVals))]
		}
		setStringField(field, val)
	}
}

// applyFormulas applies post-generation formula transformations to applicable fields.
// Null fields are left unchanged.
func applyFormulas(rv reflect.Value, cols []formulaCol) {
	for _, fc := range cols {
		field := rv.Field(fc.index)
		var currentVal string
		if field.Kind() == reflect.Ptr {
			if field.IsNil() {
				continue // null field: skip formula
			}
			currentVal = field.Elem().String()
		} else {
			currentVal = field.String()
		}
		newVal := applyFormula(currentVal, fc.formula)
		setStringField(field, newVal)
	}
}

// applyNullBlank applies null_percentage and blank overrides to nullable fields.
// null_percentage takes precedence over blank.
func applyNullBlank(rv reflect.Value, cols []nullableCol) {
	for _, nc := range cols {
		field := rv.Field(nc.index)
		if nc.nullPercentage > 0 && rand.Int63n(100) < nc.nullPercentage {
			// nil *string marshals as JSON null
			field.Set(reflect.Zero(field.Type()))
			continue
		}
		if nc.blank > 0 && rand.Int63n(100) < nc.blank {
			empty := ""
			field.Set(reflect.ValueOf(&empty))
		}
	}
}

// ---------------------------------------------------------------------------
// Struct construction
// ---------------------------------------------------------------------------

// buildStructFields constructs the dynamic struct field list along with nullable,
// custom, and formula column descriptors derived from the config.
func buildStructFields(config mockagen.MockagenConfig) ([]reflect.StructField, []nullableCol, []customCol, []formulaCol) {
	var structArr []reflect.StructField
	var nullableCols []nullableCol
	var customCols []customCol
	var formulaCols []formulaCol

	for i, col := range config.Columns {
		name := strings.ToUpper(col.Name)
		lower := strings.ToLower(col.Name)
		isNullable := col.NullPercentage > 0 || col.Blank > 0
		isCustom := isCustomCol(col)

		var fieldType reflect.Type
		if isNullable {
			fieldType = reflect.TypeOf((*string)(nil)) // *string marshals as null when nil
		} else {
			fieldType = reflect.TypeOf("")
		}

		fakerStr := ""
		if isCustom {
			fakerStr = "-" // tell faker to skip this field
		} else {
			fakerStr = mapToFaker[col.Type]
			if col.Type == "Custom List" {
				fakerStr += strings.Join(col.Values, ",")
			}
		}

		tagStr := fmt.Sprintf(`faker:"%s" json:"%s" yaml:"%s" csv:"%s"`, fakerStr, lower, lower, lower)
		if config.FileFormat == "fixed" || config.FileFormat == "parquet" {
			tagStr += fmt.Sprintf(` fixed:"%d,%d"`, col.StartPosition, col.EndPosition)
		}

		structArr = append(structArr, reflect.StructField{
			Name: name,
			Type: fieldType,
			Tag:  reflect.StructTag(tagStr),
		})

		if isNullable {
			nullableCols = append(nullableCols, nullableCol{
				index:          i,
				nullPercentage: col.NullPercentage,
				blank:          col.Blank,
			})
		}

		if isCustom {
			cc := customCol{
				index:     i,
				colType:   col.Type,
				isPointer: isNullable,
			}
			switch col.Type {
			case "Datetime":
				if col.Min != "" {
					if t, err := parseDateFlex(col.Min); err == nil {
						cc.minTime = t.UTC()
					}
				}
				if col.Max != "" {
					if t, err := parseDateFlex(col.Max); err == nil {
						cc.maxTime = t.UTC()
					}
				}
				if cc.minTime.After(cc.maxTime) {
					cc.minTime, cc.maxTime = cc.maxTime, cc.minTime
				}
				layout := col.Format
				if layout == "" {
					layout = "2006-01-02"
				} else {
					layout = strftimeToGoLayout(layout)
				}
				cc.goLayout = layout

			case "Number":
				if col.Min != "" {
					if v, err := strconv.ParseFloat(col.Min, 64); err == nil {
						cc.minVal = v
					}
				}
				if col.Max != "" {
					if v, err := strconv.ParseFloat(col.Max, 64); err == nil {
						cc.maxVal = v
					}
				}
				if col.Min == "" && col.Max == "" {
					cc.maxVal = 1000
				}
				cc.decimals = col.Decimals
				cc.isFloat = col.Decimals > 0

			case "City", "State", "Zip Code", "Street Address":
				cc.colType = "address"
				cc.addressField = col.Type

			default:
				if vals, ok := listValues[col.Type]; ok {
					cc.colType = "list"
					cc.listVals = vals
				}
			}
			customCols = append(customCols, cc)
		}

		if col.Formula != "" {
			formulaCols = append(formulaCols, formulaCol{index: i, formula: col.Formula})
		}
	}

	return structArr, nullableCols, customCols, formulaCols
}

// ---------------------------------------------------------------------------
// CSV helpers
// ---------------------------------------------------------------------------

// recordToStrings extracts all field values from a dynamic struct as strings.
// Nil *string fields (nulls) are written as empty strings in CSV.
func recordToStrings(rv reflect.Value) []string {
	n := rv.NumField()
	row := make([]string, n)
	for i := 0; i < n; i++ {
		f := rv.Field(i)
		if f.Kind() == reflect.Ptr {
			if !f.IsNil() {
				row[i] = f.Elem().String()
			}
		} else {
			row[i] = f.String()
		}
	}
	return row
}

// ---------------------------------------------------------------------------
// Core generation loop
// ---------------------------------------------------------------------------

// streamFakes generates all fake records and writes them directly to w in the
// configured file format. Records are written as they are produced; the full
// dataset is never held in memory.
func streamFakes(config mockagen.MockagenConfig, w io.Writer) {
	structArr, nullableCols, customCols, formulaCols := buildStructFields(config)
	structType := reflect.StructOf(structArr)

	fakesCh := make(chan any)
	writerDone := make(chan struct{})

	// Writer goroutine: drains fakesCh and encodes records directly to w.
	go func() {
		defer close(writerDone)
		switch config.FileFormat {
		case "json":
			w.Write([]byte("["))
			first := true
			for record := range fakesCh {
				b, err := json.Marshal(record)
				if err != nil {
					panic(err)
				}
				if !first {
					w.Write([]byte(","))
				}
				w.Write(b)
				first = false
			}
			w.Write([]byte("]"))

		case "yaml":
			enc := yaml.NewEncoder(w)
			for record := range fakesCh {
				if err := enc.Encode(record); err != nil {
					panic(err)
				}
			}
			enc.Close()

		case "csv":
			cw := csv.NewWriter(w)
			if config.IncludeHeader {
				headers := make([]string, len(config.Columns))
				for i, col := range config.Columns {
					headers[i] = col.Name
				}
				if err := cw.Write(headers); err != nil {
					panic(err)
				}
			}
			for record := range fakesCh {
				rv := reflect.ValueOf(record).Elem()
				if err := cw.Write(recordToStrings(rv)); err != nil {
					panic(err)
				}
			}
			cw.Flush()

		case "fixed", "parquet":
			for record := range fakesCh {
				b, err := fixed.Marshal([]any{record})
				if err != nil {
					panic(err)
				}
				w.Write(b)
			}
		}
	}()

	var wg sync.WaitGroup
	numOfWorkers := 48
	if config.NumberOfRecords < numOfWorkers {
		numOfWorkers = config.NumberOfRecords
	}
	recordsPerGo := config.NumberOfRecords / numOfWorkers
	remainder := config.NumberOfRecords % numOfWorkers

	wg.Add(numOfWorkers)
	for i := 0; i < numOfWorkers; i++ {
		// Distribute the remainder: the first `remainder` workers generate one extra record.
		count := recordsPerGo
		if i < remainder {
			count++
		}
		go func(count int) {
			defer wg.Done()
			for x := 0; x < count; x++ {
				// Allocate a fresh struct per record to avoid aliasing races
				// when the writer goroutine marshals records concurrently.
				fakerInterface := reflect.New(structType).Interface()
				if err := faker.FakeData(&fakerInterface); err != nil {
					panic(err)
				}
				rv := reflect.ValueOf(fakerInterface).Elem()
				applyCustomCols(rv, customCols)  // fill custom-generated fields
				applyFormulas(rv, formulaCols)   // apply formula transforms
				applyNullBlank(rv, nullableCols) // apply null/blank overrides
				fakesCh <- reflect.ValueOf(fakerInterface).Interface()
			}
		}(count)
	}

	go func() {
		wg.Wait()
		close(fakesCh)
	}()

	<-writerDone
}

func main() {
	flag.StringVar(&configFile, "config", "", "path to mockagen config file")
	flag.Parse()

	config := mockagen.LoadConfig(configFile)

	if err := os.MkdirAll("./output", os.ModePerm); err != nil {
		panic(err)
	}
	outputPath := fmt.Sprintf("./output/%s.%s", strings.ReplaceAll(config.Name, " ", "-"), config.FileFormat)

	f, err := os.Create(outputPath)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	bw := bufio.NewWriter(f)
	streamFakes(config, bw)
	if err := bw.Flush(); err != nil {
		panic(err)
	}
}
