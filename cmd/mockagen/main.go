package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"unicode"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/go-faker/faker/v4"
	fixed "github.com/ianlopshire/go-fixedwidth"
	yaml "gopkg.in/yaml.v3"
)

var configFile string
var outputFile string
var mapToFaker = map[string]string{
	"GUID":          "uuid_hyphenated",
	"First Name":    "first_name",
	"Last Name":     "last_name",
	"Email Address": "email",
	"Gender":        "oneof: male,female",
	"Datetime":      "date",
	"Custom List":   "oneof:",
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
	wg.Add(numOfWorkers)
	for i := 0; i < numOfWorkers; i++ {
		go func() {
			fakerInterface := reflect.New(reflect.StructOf(structArr)).Interface()
			for x := 0; x < recordsPerGo; x++ {
				err := faker.FakeData(&fakerInterface)
				if err != nil {
					panic(err)
				}
				fakesCh <- reflect.ValueOf(fakerInterface).Interface()
			}
			wg.Done()
		}()
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
