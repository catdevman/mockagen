package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"reflect"
	"strings"
	"sync"
	"unicode"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/catdevman/mockagen/pkg/mockagen/provider"
	"github.com/go-faker/faker/v4"
	fixed "github.com/ianlopshire/go-fixedwidth"
	yaml "gopkg.in/yaml.v3"
)

var configFile string

// mapToFaker maps Mockaroo schema type names to go-faker struct tags.
// Only faker tags that resolve to a plain string are safe here: every
// generated struct field is typed as Go string (see generateFakes)
var mapToFaker = map[string]string{
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
	"Word":                 "word",
	"Sentence":             "sentence",
	"Paragraph":            "paragraph",
	"Title (Male)":         "title_male",
	"Title (Female)":       "title_female",
}

// mockagen-specific types that go-faker has no built-in generator for
// (registered via faker.AddProvider in pkg/mockagen/provider's init).
func init() {
	maps.Copy(mapToFaker, provider.TypeMap)
}

func main() {
	// Need arguments:
	flag.StringVar(&configFile, "config", "", "")
	flag.Parse()
	// Check if config is legit
	config := mockagen.LoadConfig(configFile)
	outputFile := fmt.Sprintf("./output/%s.%s", strings.ReplaceAll(config.Name, " ", "-"), config.FileFormat)
	fakes := generateFakes(config)

	switch config.FileFormat {
	case "yaml":
		fakerBytes, err := yaml.Marshal(fakes)
		if err != nil {
			panic(err)
		}
		err = os.WriteFile(outputFile, fakerBytes, os.ModePerm)
		if err != nil {
			panic(err)
		}
	case "json":
		fakerBytes, err := json.Marshal(fakes)
		if err != nil {
			panic(err)
		}
		err = os.WriteFile(outputFile, fakerBytes, os.ModePerm)
		if err != nil {
			panic(err)
		}
	case "fixed":
		fakerBytes, err := fixed.Marshal(fakes)
		if err != nil {
			panic(err)
		}
		err = os.WriteFile(outputFile, fakerBytes, os.ModePerm)
		if err != nil {
			panic(err)
		}
	case "parquet":
		fakerBytes, err := fixed.Marshal(fakes)
		if err != nil {
			panic(err)
		}
		err = os.WriteFile(outputFile, fakerBytes, os.ModePerm)
		if err != nil {
			panic(err)
		}

	}
}

// structFieldName turns an arbitrary column name into a valid, unique
// exported Go struct field identifier for use with reflect.StructOf.
func structFieldName(colName string, idx int) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(colName) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" || unicode.IsDigit(rune(name[0])) {
		name = "F" + name
	}
	return fmt.Sprintf("%s_%d", name, idx)
}

func generateFakes(config mockagen.MockagenConfig) []any {
	structArr := []reflect.StructField{}
	for i, col := range config.Columns {
		// Map col to faker type to create reflected Struct
		name := structFieldName(col.Name, i)
		fakerStr := mapToFaker[col.Type]
		if col.Type == "Custom List" {
			fakerStr += strings.Join(col.Values, ",")
		}
		lower := strings.ToLower(col.Name)
		tagStr := fmt.Sprintf("faker:\"%s\" json:\"%s\" yaml:\"%s\" csv:\"%s\"", fakerStr, lower, lower, lower)
		if config.FileFormat == "fixed" {
			tagStr += fmt.Sprintf(" fixed:\"%d,%d\"", col.StartPosition, col.EndPosition)
		}
		tag := reflect.StructTag(tagStr) // This should also have info for json, csv, yaml tags
		t := reflect.TypeOf("")

		structArr = append(structArr, reflect.StructField{
			Name: name,
			Type: t,
			Tag:  tag,
		})
	}

	var fakes = []any{}
	if config.NumberOfRecords <= 0 {
		return fakes
	}
	fakesCh := make(chan any)
	var wg sync.WaitGroup
	numOfWorkers := 48
	if config.NumberOfRecords < numOfWorkers {
		numOfWorkers = config.NumberOfRecords
	}
	recordsPerGo := config.NumberOfRecords / numOfWorkers
	remainder := config.NumberOfRecords % numOfWorkers
	wg.Add(numOfWorkers)
	for i := 0; i < numOfWorkers; i++ {
		n := recordsPerGo
		if i < remainder {
			n++
		}
		go func(n int) {
			fakerInterface := reflect.New(reflect.StructOf(structArr)).Interface()
			for x := 0; x < n; x++ {
				err := faker.FakeData(&fakerInterface)
				if err != nil {
					panic(err)
				}
				fakesCh <- reflect.ValueOf(fakerInterface).Interface()
			}
			wg.Done()
		}(n)
	}
	go func() {
		wg.Wait()
		close(fakesCh)
	}()

	for fake := range fakesCh {
		fakes = append(fakes, fake)
	}
	return fakes
}
