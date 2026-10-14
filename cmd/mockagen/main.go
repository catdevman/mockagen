package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"unicode"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/catdevman/mockagen/pkg/mockagen/types"
	"github.com/go-faker/faker/v4"
)

var (
	// version, commit, and date are injected at build time by .goreleaser.yaml
	// via ldflags. When compiled directly without ldflags, default placeholders
	// keep mockagen version informative in local and development builds.
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// validSubcommands enumerates the supported first-class verbs. An unknown
// verb not prefixed with a '-' flag marker is rejected naming these options.
var validSubcommands = []string{"gen", "schema", "map", "import", "version", "help"}

// mapToFaker mirrors types.FakerTags() so generation routines and test guards
// retain direct map access while pkg/mockagen/types serves as the single
// source of truth for generator type definitions and SQL affinities.
var mapToFaker = types.FakerTags()

func main() {
	if code := runCLI(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

// runCLI parses arguments and dispatches to subcommands or the legacy flag
// fallback. Extracted from main so dispatch, usage messages and exit codes can
// be tested in-process without spawning subprocesses.
func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 1
	}

	subcmd := args[0]
	switch subcmd {
	case "gen":
		genFs := flag.NewFlagSet("gen", flag.ContinueOnError)
		genFs.SetOutput(stderr)
		var cfg, out string
		genFs.StringVar(&cfg, "config", "", "path to schema configuration file")
		genFs.StringVar(&out, "output", "", "path to output file")
		if err := genFs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if cfg == "" {
			fmt.Fprintln(stderr, "mockagen gen: -config is required")
			printUsage(stderr)
			return 1
		}
		return runGenerate(cfg, out, stderr)

	case "schema":
		schemaFs := flag.NewFlagSet("schema", flag.ContinueOnError)
		schemaFs.SetOutput(stderr)
		if err := schemaFs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		fmt.Fprintln(stderr, "mockagen: schema command not yet implemented")
		return 1

	case "map":
		mapFs := flag.NewFlagSet("map", flag.ContinueOnError)
		mapFs.SetOutput(stderr)
		if err := mapFs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		fmt.Fprintln(stderr, "mockagen: map command not yet implemented")
		return 1

	case "import":
		importFs := flag.NewFlagSet("import", flag.ContinueOnError)
		importFs.SetOutput(stderr)
		if err := importFs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		fmt.Fprintln(stderr, "mockagen: import command not yet implemented")
		return 1

	case "version":
		versionFs := flag.NewFlagSet("version", flag.ContinueOnError)
		versionFs.SetOutput(stderr)
		if err := versionFs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		printVersion(stdout)
		return 0

	case "help":
		helpFs := flag.NewFlagSet("help", flag.ContinueOnError)
		helpFs.SetOutput(stderr)
		if err := helpFs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		printUsage(stdout)
		return 0

	default:
		if strings.HasPrefix(subcmd, "-") {
			legacyFs := flag.NewFlagSet("mockagen", flag.ContinueOnError)
			legacyFs.SetOutput(stderr)
			var cfg, out string
			var showVer bool
			legacyFs.StringVar(&cfg, "config", "", "path to schema configuration file")
			legacyFs.StringVar(&out, "output", "", "path to output file")
			legacyFs.BoolVar(&showVer, "version", false, "print version information")
			if err := legacyFs.Parse(args); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					printUsage(stdout)
					return 0
				}
				return 2
			}
			if showVer {
				printVersion(stdout)
				return 0
			}
			if cfg == "" {
				printUsage(stderr)
				return 1
			}
			return runGenerate(cfg, out, stderr)
		}

		fmt.Fprintf(stderr, "mockagen: unknown subcommand %q. Valid subcommands: %s\n\n",
			subcmd, strings.Join(validSubcommands, ", "))
		printUsage(stderr)
		return 1
	}
}

// runGenerate executes fake data generation and writes records to outPath.
// Centralised so gen subcommand and legacy flag dispatch share the exact same
// generation logic and error reporting.
func runGenerate(cfgPath, outPath string, errOutput io.Writer) int {
	config, err := mockagen.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(errOutput, "mockagen:", err)
		return 1
	}
	out := outputPath(config, outPath)
	if err := ensureOutputDir(out); err != nil {
		fmt.Fprintln(errOutput, "mockagen:", err)
		return 1
	}

	fakesCh, structArr := generateFakes(config)
	w, err := newRecordWriter(config, structArr, out)
	if err != nil {
		fmt.Fprintln(errOutput, "mockagen:", err)
		return 1
	}
	for fake := range fakesCh {
		if err := w.WriteRecord(fake); err != nil {
			fmt.Fprintln(errOutput, "mockagen:", err)
			return 1
		}
	}
	if err := w.Close(); err != nil {
		fmt.Fprintln(errOutput, "mockagen:", err)
		return 1
	}
	return 0
}

// printVersion writes version, commit and build date metadata to w.
// .goreleaser.yaml injects all three ldflags during releases.
func printVersion(w io.Writer) {
	fmt.Fprintf(w, "mockagen version %s (commit: %s, date: %s)\n", version, commit, date)
}

// printUsage outputs the list of available commands and general synopsis to w.
// Shared across bare invocations, the help subcommand, and CLI parse errors.
func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: mockagen <command> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  gen      generate fake data from a schema config")
	fmt.Fprintln(w, "  schema   introspect a database table and write a schema file")
	fmt.Fprintln(w, "  map      map source fields to target schema columns")
	fmt.Fprintln(w, "  import   import source data using a field map")
	fmt.Fprintln(w, "  version  print version and build metadata")
	fmt.Fprintln(w, "  help     show command usage")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Run 'mockagen <command> -help' for command-specific flags.")
}

// outputPath selects the destination for generated records: an explicit
// destination overrides the historical convention of placing files under
// ./output/ with spaces converted to dashes.
func outputPath(config mockagen.MockagenConfig, explicit string) string {
	if explicit != "" {
		return explicit
	}
	name := strings.ReplaceAll(config.Name, " ", "-")
	return fmt.Sprintf("./output/%s.%s", name, outputExt(config.FileFormat))
}

// ensureOutputDir ensures the parent directory of path exists before a writer
// attempts to create the output file. Writing fails outright on os.Create if
// the target directory has not been created beforehand.
func ensureOutputDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

// outputExt returns the extension the output file gets for a file_format.
// Most format names double as a sensible extension; "fluent" names a line
// format rather than a file type, so its output lands in a .log file.
func outputExt(format string) string {
	if format == "fluent" {
		return "log"
	}
	return format
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
		tagStr := fmt.Sprintf("faker:\"%s\" json:\"%s\" yaml:\"%s\" csv:\"%s\" parquet:\"%s\" sql:\"%s\"", fakerStr, lower, lower, lower, lower, lower)
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
