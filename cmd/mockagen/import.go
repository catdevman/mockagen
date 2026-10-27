package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
)

// runImport loads a field map and writes the rows it describes. Flags are
// parsed here, as in runMap, so help text and exit codes can be tested
// without the other subcommands. A bad header or an unclean plan returns
// before the output file is created: either one would otherwise leave a
// partial load that looks like success.
func runImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var mapPath, dataPath, outPath, rejects string
	var limit int
	var dry bool
	fs.StringVar(&mapPath, "map", "", "field map")
	fs.StringVar(&dataPath, "data", "", "source data file")
	fs.StringVar(&outPath, "out", "", "output file (default SQL on stdout)")
	fs.StringVar(&rejects, "rejects", "", "CSV of rows that failed, with line and reason")
	fs.IntVar(&limit, "limit", 0, "stop after this many source rows")
	fs.BoolVar(&dry, "dry-run", false, "check rows and write no output")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "mockagen import: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if mapPath == "" || dataPath == "" {
		fmt.Fprintln(stderr, "mockagen import: -map and -data are required")
		return 2
	}
	if limit < 0 {
		fmt.Fprintln(stderr, "mockagen import: -limit must be >= 0")
		return 2
	}

	fm, err := importer.Load(mapPath)
	if err != nil {
		fmt.Fprintf(stderr, "mockagen import: %v\n", err)
		return 1
	}
	schemaPath := importer.ResolveSchema(mapPath, fm.Schema)
	sch, err := schema.Load(schemaPath)
	if err != nil {
		fmt.Fprintf(stderr, "mockagen import: %v\n", err)
		return 1
	}
	pol, err := importer.Policy(fm.OnError)
	if err != nil {
		fmt.Fprintf(stderr, "mockagen import: %v\n", err)
		return 1
	}
	plan, diags := importer.Compile(sch, fm)
	if len(diags) > 0 {
		for _, d := range diags {
			fmt.Fprintf(stderr, "mockagen import: %s\n", d.Error())
		}
		return 1
	}

	src, err := importer.OpenSource(dataPath, fm.Source)
	if err != nil {
		fmt.Fprintf(stderr, "mockagen import: %v\n", err)
		return 1
	}
	defer src.Close()
	if err := importer.MatchHeader(fm.Source, src); err != nil {
		fmt.Fprintf(stderr, "mockagen import: %v\n", err)
		return 1
	}

	var out io.Writer = stdout
	var outFile *os.File
	if !dry && outPath != "" {
		if dir := filepath.Dir(outPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fmt.Fprintf(stderr, "mockagen import: %v\n", err)
				return 1
			}
		}
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintf(stderr, "mockagen import: %v\n", err)
			return 1
		}
		outFile = f
		defer outFile.Close()
		out = f
	}
	var sink importer.Sink
	if dry {
		sink = importer.DiscardSink{}
		fmt.Fprintln(stderr, "mockagen import: dry run")
	} else {
		sink = sinkFor(out, outPath)
	}

	rep, runErr := importer.Run(context.Background(), src, plan, sink, importer.Options{
		OnError: pol,
		Limit:   limit,
	})
	if rep == nil {
		fmt.Fprintf(stderr, "mockagen import: %v\n", runErr)
		return 1
	}
	if rejects != "" {
		if err := writeRejects(rejects, rep.Errors); err != nil {
			fmt.Fprintf(stderr, "mockagen import: %v\n", err)
			return 1
		}
	}
	fmt.Fprintf(stderr, "import: read %d, wrote %d, skipped %d, errors %d\n",
		rep.Read, rep.Written, rep.Skipped, len(rep.Errors))
	for _, e := range rep.Errors {
		fmt.Fprintf(stderr, "import: %s\n", e.Error())
	}
	if runErr != nil && !errors.Is(runErr, errAlreadyReported(rep)) {
		fmt.Fprintf(stderr, "mockagen import: %v\n", runErr)
	}
	if runErr != nil || len(rep.Errors) > 0 {
		return 1
	}
	return 0
}

// errAlreadyReported is a stand-in so the row error Run returns is not
// printed twice: once as a located line and again as the function error.
func errAlreadyReported(rep *importer.Report) error {
	if rep == nil || len(rep.Errors) == 0 {
		return nil
	}
	return rep.Errors[len(rep.Errors)-1].Err
}

func sinkFor(w io.Writer, path string) importer.Sink {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return importer.NewCSVSink(w, false)
	case ".json", ".jsonl":
		return importer.NewJSONSink(w)
	default:
		return importer.NewSQLSink(w)
	}
}

func writeRejects(path string, errs []importer.RowError) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := w.Write([]string{"line", "target", "reason"}); err != nil {
		return err
	}
	for _, e := range errs {
		reason := ""
		if e.Err != nil {
			reason = e.Err.Error()
		}
		if err := w.Write([]string{strconv.FormatInt(e.Line, 10), e.Target, reason}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}
