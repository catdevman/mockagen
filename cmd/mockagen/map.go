package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/catdevman/mockagen/internal/tui/mapui"
	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
)

// runMap proposes a field map from a schema file and a sample of a data
// file. Flags are parsed here, not in runCLI, so the help text and the exit
// codes can be tested without the other subcommands' flag sets.
func runMap(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("map", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var schemaPath, dataPath, mapPath, outPath string
	var sampleN int
	var tui bool
	fs.StringVar(&schemaPath, "schema", "", "path to the target schema file")
	fs.StringVar(&dataPath, "data", "", "path to the source data file")
	fs.StringVar(&mapPath, "map", "", "existing field map to extend")
	fs.StringVar(&outPath, "out", "", "where to write the field map (default stdout)")
	fs.IntVar(&sampleN, "sample", 1000, "leading data rows to read after the header")
	// -tui is opt-in. A pipe or a script keeps writing the file and
	// exiting. Missing file flags are asked only on that screen.
	fs.BoolVar(&tui, "tui", false, "open the mapping screen; prompt for missing file flags")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "mockagen map: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if sampleN < 0 {
		fmt.Fprintln(stderr, "mockagen map: -sample must be >= 0")
		return 2
	}
	// Without -tui a missing file is a usage error. The screen is the
	// only place that may ask, so a script never opens a picker.
	if !tui && (schemaPath == "" || dataPath == "") {
		fmt.Fprintln(stderr, "mockagen map: -schema and -data are required")
		return 2
	}

	var sch schema.Schema
	if schemaPath != "" {
		loaded, err := schema.Load(schemaPath)
		if err != nil {
			fmt.Fprintf(stderr, "mockagen map: %v\n", err)
			return 1
		}
		sch = loaded
	}
	// The map is loaded before the sample. A fixed-width layout lives
	// on source.fixed, and sampling first would have to guess columns
	// the file does not name.
	var base *importer.FieldMap
	if mapPath != "" {
		loaded, err := importer.Load(mapPath)
		if err != nil {
			fmt.Fprintf(stderr, "mockagen map: %v\n", err)
			return 1
		}
		base = &loaded
	}

	var sample importer.Sample
	if dataPath != "" {
		var spec *importer.SourceSpec
		if base != nil {
			spec = &base.Source
		}
		read, err := importer.ReadSampleSpec(dataPath, spec, sampleN)
		if err != nil {
			fmt.Fprintf(stderr, "mockagen map: %v\n", err)
			return 1
		}
		sample = read
	}
	if base != nil && dataPath != "" && !importer.HeaderMatches(base.Source, sample) {
		return reportHeaderDrift(outPath, stdout, stderr, *base, sample)
	}

	if tui {
		sess := mapui.Session{Schema: sch, Sample: sample, OutPath: outPath}
		if schemaPath == "" || dataPath == "" || mapPath == "" {
			sess.Ask = &mapui.Ask{
				SchemaPath: schemaPath,
				DataPath:   dataPath,
				MapPath:    mapPath,
				SampleN:    sampleN,
				NeedSchema: schemaPath == "",
				NeedData:   dataPath == "",
				NeedMap:    mapPath == "",
				Base:       base,
				MapFromCLI: mapPath != "",
			}
		} else {
			proposed := importer.Propose(sch, sample, base)
			proposed.Schema = importer.SchemaRef(outPath, schemaPath)
			sess.Map = proposed
		}
		// s writes from inside the screen and does not close it. Save
		// is only the stdout case, where there is no file until the
		// screen is gone. A quit with neither has nothing to report.
		res, err := runMapScreen(sess)
		return finishTUI(res, err, sch, schemaPath, outPath, stdout, stderr)
	}
	proposed := importer.Propose(sch, sample, base)
	proposed.Schema = importer.SchemaRef(outPath, schemaPath)
	_, diags := importer.Compile(sch, proposed)
	for _, d := range diags {
		fmt.Fprintf(stderr, "mockagen map: %s\n", d.Error())
	}
	if err := writeMap(outPath, stdout, proposed); err != nil {
		fmt.Fprintf(stderr, "mockagen map: %v\n", err)
		return 1
	}
	if len(diags) > 0 {
		return 1
	}
	return 0
}

// runMapScreen is the mapping screen. Tests replace it because a
// bubbletea program needs a terminal, and the flag rules are what this
// file is responsible for.
var runMapScreen = mapui.Run

// reportHeaderDrift prints the mismatch and writes the map unchanged.
// Propose matches by name, so rewriting here would retarget a swapped
// file and look like a successful re-run.
func reportHeaderDrift(outPath string, stdout, stderr io.Writer, m importer.FieldMap, sample importer.Sample) int {
	fmt.Fprintf(stderr, "mockagen map: source header does not match the field map\nmap: %s\ndata: %s\n",
		strings.Join(m.Source.Fields, ","), strings.Join(sample.Fields, ","))
	if err := writeMap(outPath, stdout, m); err != nil {
		fmt.Fprintf(stderr, "mockagen map: %v\n", err)
		return 1
	}
	return 1
}

// finishTUI applies the screen's result. A checkpoint was already
// written. A drift result is a -map the screen refused to edit.
func finishTUI(res mapui.Result, err error, sch schema.Schema, schemaPath, outPath string, stdout, stderr io.Writer) int {
	if err != nil {
		fmt.Fprintf(stderr, "mockagen map: %v\n", err)
		return 1
	}
	if res.Drift {
		return reportHeaderDrift(outPath, stdout, stderr, res.Map, res.Sample)
	}
	if !res.Save && !res.Checkpoint {
		return 0
	}
	if res.SchemaPath != "" {
		schemaPath = res.SchemaPath
	}
	if len(res.Schema.Columns) > 0 || res.Schema.Name != "" {
		sch = res.Schema
	}
	proposed := res.Map
	proposed.Schema = importer.SchemaRef(outPath, schemaPath)
	if !res.Save {
		_, diags := importer.Compile(sch, proposed)
		for _, d := range diags {
			fmt.Fprintf(stderr, "mockagen map: %s\n", d.Error())
		}
		if len(diags) > 0 {
			return 1
		}
		return 0
	}
	_, diags := importer.Compile(sch, proposed)
	for _, d := range diags {
		fmt.Fprintf(stderr, "mockagen map: %s\n", d.Error())
	}
	if err := writeMap(outPath, stdout, proposed); err != nil {
		fmt.Fprintf(stderr, "mockagen map: %v\n", err)
		return 1
	}
	if len(diags) > 0 {
		return 1
	}
	return 0
}

func writeMap(outPath string, stdout io.Writer, m importer.FieldMap) error {
	ext := ".yaml"
	if outPath != "" {
		if e := strings.ToLower(filepath.Ext(outPath)); e != "" {
			ext = e
		}
		if err := ensureOutputDir(outPath); err != nil {
			return err
		}
		return importer.Save(outPath, m)
	}
	data, err := importer.Marshal(m, ext)
	if err != nil {
		return err
	}
	_, err = stdout.Write(data)
	return err
}
