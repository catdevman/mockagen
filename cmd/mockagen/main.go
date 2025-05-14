package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/catdevman/mockagen/pkg/mockagen"
	goparquet "github.com/fraugster/parquet-go"
	"github.com/fraugster/parquet-go/floor"
	"github.com/fraugster/parquet-go/parquet"
	"github.com/fraugster/parquet-go/parquetschema"
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
	fakes, fakeType := generateFakes(config)

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
		schema, _ := createParquetSchema(fakeType)
		writer, _ := floor.NewFileWriter(outputFile,
			goparquet.WithSchemaDefinition(schema),
			goparquet.WithCompressionCodec(parquet.CompressionCodec_SNAPPY),
		)
		for _, fake := range fakes {
			if err := writer.Write(fake); err != nil {
				log.Fatalf("Writing record failed: %v", err)
			}
		}
		if err := writer.Close(); err != nil {
			log.Fatalf("Closing parquet writer failed: %v", err)
		}

	}
}

// createParquetSchema creates a Parquet schema from the config.
func createParquetSchema(fields []reflect.StructField) (*parquetschema.SchemaDefinition, error) {
	var columns []*parquetschema.ColumnDefinition
	for _, f := range fields {
		// Extract Parquet tag (e.g., `parquet:"name=ID, type=INT64"`)
		tag := f.Tag.Get("parquet")
		if tag == "" {
			return nil, fmt.Errorf("missing parquet tag for field %s", f.Name)
		}
		// Parse tag to get name and type
		var colName, colType string
		for _, part := range strings.Split(tag, ",") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) != 2 {
				continue
			}
			switch kv[0] {
			case "name":
				colName = kv[1]
			case "type":
				colType = kv[1]
			}
		}
		if colName == "" || colType == "" {
			return nil, fmt.Errorf("invalid parquet tag for field %s: %s", f.Name, tag)
		}

		col := &parquetschema.ColumnDefinition{
			SchemaElement: &parquet.SchemaElement{
				Name:           colName,
				RepetitionType: parquet.FieldRepetitionTypePtr(parquet.FieldRepetitionType_REQUIRED),
			},
		}
		switch colType {
		case "INT64":
			if f.Type != reflect.TypeOf(int64(0)) {
				return nil, fmt.Errorf("field %s type %v does not match INT64", f.Name, f.Type)
			}
			col.SchemaElement.Type = parquet.TypePtr(parquet.Type_INT64)
		case "BYTE_ARRAY":
			if f.Type != reflect.TypeOf("") {
				return nil, fmt.Errorf("field %s type %v does not match BYTE_ARRAY", f.Name, f.Type)
			}
			col.SchemaElement.Type = parquet.TypePtr(parquet.Type_BYTE_ARRAY)
			col.SchemaElement.ConvertedType = parquet.ConvertedTypePtr(parquet.ConvertedType_UTF8)
		case "DOUBLE":
			if f.Type != reflect.TypeOf(float64(0)) {
				return nil, fmt.Errorf("field %s type %v does not match DOUBLE", f.Name, f.Type)
			}
			col.SchemaElement.Type = parquet.TypePtr(parquet.Type_DOUBLE)
		default:
			return nil, fmt.Errorf("unsupported parquet type %s for field %s", colType, f.Name)
		}
		columns = append(columns, col)
	}
	return &parquetschema.SchemaDefinition{
		RootColumn: &parquetschema.ColumnDefinition{
			SchemaElement: &parquet.SchemaElement{
				Name:           "record",
				RepetitionType: parquet.FieldRepetitionTypePtr(parquet.FieldRepetitionType_REQUIRED),
			},
			Children: columns,
		},
	}, nil
}

func generateFakes(config mockagen.MockagenConfig) ([]any, []reflect.StructField) {
	structArr := []reflect.StructField{}
	for _, col := range config.Columns {
		// Map col to faker type to create reflected Struct
		name := strings.ToUpper(col.Name)
		fakerStr := mapToFaker[col.Type]
		if col.Type == "Custom List" {
			fakerStr += strings.Join(col.Values, ",")
		}
		lower := strings.ToLower(col.Name)
		tagStr := fmt.Sprintf("faker:\"%s\" json:\"%s\" yaml:\"%s\" csv:\"%s\" parquet:\"name=%s, type=BYTE_ARRAY\"", fakerStr, lower, lower, lower, lower)
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
	fakesCh := make(chan any)
	var wg sync.WaitGroup
	numOfWorkers := runtime.NumCPU()
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
	return fakes, structArr
}
