package main

import (
	"flag"
	"fmt"
	"maps"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"unicode"

	"github.com/catdevman/faker"
	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/catdevman/mockagen/pkg/mockagen/provider"
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

	fakesCh, structArr := generateFakes(config)
	w, err := newRecordWriter(config, structArr, outputFile)
	if err != nil {
		panic(err)
	}
	for fake := range fakesCh {
		if err := w.WriteRecord(fake); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
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

// generateFakes builds the reflected record struct for config.Columns, then
// streams config.NumberOfRecords fake records through the returned channel
// via a worker pool. Consuming the channel (rather than collecting it into
// a slice) keeps memory bounded regardless of how many records are requested.
func generateFakes(config mockagen.MockagenConfig) (<-chan any, []reflect.StructField) {
	structArr := make([]reflect.StructField, 0, len(config.Columns))
	for i, col := range config.Columns {
		// Map col to faker type to create reflected Struct
		name := structFieldName(col.Name, i)
		fakerStr := mapToFaker[col.Type]
		if col.Type == "Custom List" {
			fakerStr += strings.Join(col.Values, ",")
		}
		lower := strings.ToLower(col.Name)
		tagStr := fmt.Sprintf("faker:\"%s\" json:\"%s\" yaml:\"%s\" csv:\"%s\" parquet:\"%s\"", fakerStr, lower, lower, lower, lower)
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

	// recordBufferSize gives the writer's consumer loop room to lag behind
	// the worker pool. An unbuffered channel forces every worker to block
	// on each send until the single consumer finishes marshaling *and
	// writing* the previous record, which serializes generation behind
	// disk I/O - the buffer restores the pool's parallelism while keeping
	// memory bounded to a small, fixed number of records rather than the
	// whole run.
	const recordBufferSize = 4096
	fakesCh := make(chan any, recordBufferSize)
	if config.NumberOfRecords <= 0 {
		close(fakesCh)
		return fakesCh, structArr
	}
	var wg sync.WaitGroup
	// Generation is bottlenecked on faker's package-level RNG, which serialises
	// every caller through a single mutex (see faker.NewSafeSource). Past a
	// handful of workers the pool spends more time contending for that lock
	// than generating records: a CPU profile of a 1000-record run put ~60% of
	// samples in mutex lock/unlock at 24 workers, and 24 workers measured
	// *slower* than a single one. GOMAXPROCS rather than NumCPU so container
	// CPU limits are respected, then capped where the contention takes over.
	const maxGenWorkers = 4
	numOfWorkers := runtime.GOMAXPROCS(0)
	if numOfWorkers > maxGenWorkers {
		numOfWorkers = maxGenWorkers
	}
	if config.NumberOfRecords < numOfWorkers {
		numOfWorkers = config.NumberOfRecords
	}
	recordsPerGo := config.NumberOfRecords / numOfWorkers
	remainder := config.NumberOfRecords % numOfWorkers
	// Built once rather than inside every worker: reflect.StructOf walks the
	// field list and consults a global type cache on each call.
	recordType := reflect.StructOf(structArr)
	wg.Add(numOfWorkers)
	for i := 0; i < numOfWorkers; i++ {
		n := recordsPerGo
		if i < remainder {
			n++
		}
		go func(n int) {
			defer wg.Done()
			fakerInterface := reflect.New(recordType).Interface()
			for x := 0; x < n; x++ {
				err := faker.FakeData(&fakerInterface)
				if err != nil {
					panic(err)
				}
				// FakeData reassigns fakerInterface to a freshly allocated
				// record each call, so the value can be sent as-is; the old
				// reflect.ValueOf(...).Interface() round-trip only unwrapped
				// and rewrapped the same pointer.
				fakesCh <- fakerInterface
			}
		}(n)
	}
	go func() {
		wg.Wait()
		close(fakesCh)
	}()

	return fakesCh, structArr
}
