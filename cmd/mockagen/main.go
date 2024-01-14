package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"

    mockagen "github.com/catdevman/mockagen/pkg"
	"github.com/bxcodec/faker/v3"
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
	// - Check if config is legit
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
	}
}

func generateFakes(config mockagen.LocaldooConfig) []any {
	structArr := []reflect.StructField{}
	for _, col := range config.Columns {
		// Map col to faker type to create reflected Struct
		name := strings.ToUpper(col.Name)
		fakerStr := mapToFaker[col.Type]
		if col.Type == "Custom List" {
			fakerStr += strings.Join(col.Values, ",")
		}
        lower := strings.ToLower(col.Name)
		tagStr := fmt.Sprintf("faker:\"%s\" json:\"%s\" yaml:\"%s\" csv:\"%s\"", fakerStr, lower, lower, lower)
		tag := reflect.StructTag(tagStr) // This should also have info for json, csv, yaml tags
		t := reflect.TypeOf("")
		// fmt.Println(name, tag, t)
		structArr = append(structArr, reflect.StructField{
			Name: name,
			Type: t,
			Tag:  tag,
		})
	}

	var fakes = []any{}
	//fakerInterface := reflect.New(reflect.StructOf(structArr)).Interface()
	fakesCh := make(chan any)
	var wg sync.WaitGroup
    numOfWorkers := 50
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
