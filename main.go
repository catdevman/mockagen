package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"strings"

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
	config := LoadConfig(configFile)
	outputFile := fmt.Sprintf("./output/%s.%s", strings.ReplaceAll(config.Name, " ", "-"), config.FileFormat)
	structArr := []reflect.StructField{}
	for _, col := range config.Columns {
		// Map col to faker type to create reflected Struct
		name := strings.ToUpper(col.Name)
		fakerStr := mapToFaker[col.Type]
		if col.Type == "Custom List" {
			fakerStr += strings.Join(col.Values, ",")
		}
		tagStr := fmt.Sprintf("faker:\"%s\" json:\"%s\" yaml:\"%s\" csv:\"%s\"", fakerStr, strings.ToLower(col.Name), strings.ToLower(col.Name), strings.ToLower(col.Name))
		tag := reflect.StructTag(tagStr) // This should also have info for json, csv, yaml tags
		t := reflect.TypeOf("")
		// fmt.Println(name, tag, t)
		structArr = append(structArr, reflect.StructField{
			Name: name,
			Type: t,
			Tag:  tag,
		})
	}

	fakerInterface := reflect.New(reflect.StructOf(structArr)).Interface()
	var fakes = []interface{}{}
	for i := 0; i < config.NumberOfRows; i++ {
		err := faker.FakeData(&fakerInterface)
		if err != nil {
			panic(err)
		}
		fakes = append(fakes, reflect.ValueOf(fakerInterface).Interface())
	}

	if strings.Compare(config.FileFormat, "yaml") == 0 {
		fakerBytes, err := yaml.Marshal(fakes)
		if err != nil {
			panic(err)
		}
		ioutil.WriteFile(outputFile, fakerBytes, os.ModePerm)
	} else if strings.Compare(config.FileFormat, "json") == 0 {
		fakerBytes, err := json.Marshal(fakes)
		if err != nil {
			panic(err)
		}
		ioutil.WriteFile(outputFile, fakerBytes, os.ModePerm)
	}

}
