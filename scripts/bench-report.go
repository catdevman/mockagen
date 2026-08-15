//go:build ignore

// Command bench-report turns `benchstat -format=csv base.txt head.txt` into a
// verdict and a Markdown report.
//
// Thresholds differ by unit on purpose:
//
//   - sec/op is wall-clock and noisy even with same-job A/B, so a regression
//     must clear a wide margin *and* be flagged significant by benchstat's
//     Mann-Whitney test (benchstat prints "~" when a difference is
//     indistinguishable from noise).
//   - B/op and allocs/op come from deterministic counters. A consistent shift
//     there is a real change in the code's behaviour, so they get a tight
//     threshold and no significance gate.
//
// It is deliberately built as a standalone script (`//go:build ignore`) so it
// stays out of `go build ./...` and `go test ./...`:
//
//	go run scripts/bench-report.go --csv compare.csv
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

type threshold struct {
	pct               float64
	needsSignificance bool
}

var thresholds = map[string]threshold{
	"sec/op":    {10.0, true},
	"B/op":      {1.0, false},
	"allocs/op": {1.0, false},
}

var defaultThreshold = threshold{10.0, true}

// benchstat column label for the baseline; the workflow passes `base=...`.
const baseFileLabel = "base"

// row is one benchmark in one unit. old or new is nil when the benchmark only
// exists on one side.
type row struct {
	name string
	old  *float64
	new  *float64
	vs   string // benchstat's "vs base" cell: "~" means indistinguishable from noise
	p    string
}

type section struct {
	unit string
	rows []row
}

type finding struct {
	unit, name string
	old, new   float64
	delta      float64
	p          string
}

func humanize(unit string, v *float64) string {
	if v == nil {
		return "-"
	}
	value := *v
	switch unit {
	case "sec/op":
		for _, s := range []struct {
			scale  float64
			suffix string
		}{{1e-9, "ns"}, {1e-6, "µs"}, {1e-3, "ms"}, {1, "s"}} {
			if value < s.scale*1000 {
				return fmt.Sprintf("%.2f %s", value/s.scale, s.suffix)
			}
		}
		return fmt.Sprintf("%.2f s", value)
	case "B/op":
		for _, s := range []struct {
			scale  float64
			suffix string
		}{{1, "B"}, {1 << 10, "KiB"}, {1 << 20, "MiB"}} {
			if value < s.scale*1024 {
				return fmt.Sprintf("%.1f %s", value/s.scale, s.suffix)
			}
		}
		return fmt.Sprintf("%.1f GiB", value/(1<<30))
	}
	return commas(value)
}

// commas formats a rounded number with thousands separators (Python's %,.0f).
func commas(v float64) string {
	s := strconv.FormatFloat(math.Round(v), 'f', 0, 64)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func num(fields []string, i int) *float64 {
	if i >= len(fields) {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(fields[i]), 64)
	if err != nil {
		return nil
	}
	return &f
}

func field(fields []string, i int) string {
	if i >= len(fields) {
		return ""
	}
	return strings.TrimSpace(fields[i])
}

func contains(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

// parse reads benchstat CSV into sections.
//
// It handles both shapes benchstat emits: the two-file comparison
// (",<unit>,CI,<unit>,CI,vs base,P") and the single-file table (",<unit>,CI")
// that appears when only one side has a given benchmark - e.g. a PR that adds a
// new benchmark, or a rename that leaves no overlap. In the single-file case one
// of old/new is nil and the row is reported as new/removed rather than a change.
func parse(r io.Reader) ([]section, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true

	var (
		sections []section
		unit     string
		rows     []row
		nFiles   = 1
		labels   []string
	)

	flush := func() {
		if unit != "" && len(rows) > 0 {
			sections = append(sections, section{unit, rows})
		}
	}

	for {
		fields, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		// Unit header: ",<unit>,CI[,<unit>,CI,vs base,P]"
		if len(fields) >= 3 && field(fields, 0) == "" && contains(fields, "CI") {
			flush()
			unit, rows = field(fields, 1), nil
			nFiles = 0
			for _, f := range fields {
				if f == "CI" {
					nFiles++
				}
			}
			continue
		}

		// The row above each unit header names the input files this table covers.
		if len(fields) > 1 && field(fields, 0) == "" && !contains(fields, "CI") {
			var found []string
			for _, f := range fields[1:] {
				if f != "" {
					found = append(found, f)
				}
			}
			if len(found) > 0 {
				labels = found
				continue
			}
		}

		if unit == "" {
			continue
		}
		name := field(fields, 0)
		if name == "" || strings.HasPrefix(name, "goos") || strings.HasPrefix(name, "goarch") ||
			strings.HasPrefix(name, "pkg") || strings.HasPrefix(name, "cpu") {
			continue
		}

		var cur row
		cur.name = name
		if nFiles >= 2 {
			cur.old, cur.new = num(fields, 1), num(fields, 3)
			cur.vs, cur.p = field(fields, 5), field(fields, 6)
		} else {
			// Single column: attribute the value to whichever side it came from,
			// so a benchmark that only exists on base reads as removed rather
			// than as a mysterious new one.
			v := num(fields, 1)
			if len(labels) > 0 && labels[0] == baseFileLabel {
				cur.old = v
			} else {
				cur.new = v
			}
		}
		if cur.old == nil && cur.new == nil {
			continue
		}
		rows = append(rows, cur)
	}
	flush()
	return sections, nil
}

func pct(old, new float64) float64 {
	if old == 0 {
		if new == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return (new - old) / old * 100.0
}

func fmtPct(d float64) string {
	if math.IsInf(d, 0) || math.IsNaN(d) {
		return "n/a"
	}
	return fmt.Sprintf("%+.2f%%", d)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func quoteJoin(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "`" + n + "`"
	}
	return strings.Join(q, ", ")
}

func main() {
	var (
		csvPath      = flag.String("csv", "", "benchstat CSV input (default stdin)")
		outPath      = flag.String("out", "", "Markdown output (default stdout)")
		baseLabel    = flag.String("base-label", "base", "label for the baseline commit")
		headLabel    = flag.String("head-label", "head", "label for the new commit")
		title        = flag.String("title", "Benchmark comparison", "report title")
		emitGoBench  = flag.String("emit-go-bench", "", "write median head results in `go test -bench` format")
		pkg          = flag.String("pkg", "github.com/catdevman/mockagen/cmd/mockagen", "package name for --emit-go-bench")
		githubOutput = flag.String("github-output", "", "append regressed=<bool> here")
		failOnReg    = flag.Bool("fail-on-regression", false, "exit 1 when a regression is found")
	)
	flag.Parse()

	in := io.Reader(os.Stdin)
	if *csvPath != "" {
		f, err := os.Open(*csvPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bench-report:", err)
			os.Exit(2)
		}
		defer f.Close()
		in = f
	}

	sections, err := parse(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench-report: parsing benchstat CSV:", err)
		os.Exit(2)
	}

	var out []string
	add := func(format string, args ...any) {
		out = append(out, fmt.Sprintf(format, args...))
	}

	writeAll := func(regressions int) {
		body := strings.Join(out, "\n") + "\n"
		if *outPath == "" {
			fmt.Print(body)
		} else if err := os.WriteFile(*outPath, []byte(body), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "bench-report:", err)
			os.Exit(2)
		}
		if *githubOutput != "" {
			f, err := os.OpenFile(*githubOutput, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				fmt.Fprintln(os.Stderr, "bench-report:", err)
				os.Exit(2)
			}
			fmt.Fprintf(f, "regressed=%t\nregression_count=%d\n", regressions > 0, regressions)
			f.Close()
		}
	}

	add("## %s", *title)
	add("")

	if len(sections) == 0 {
		add("benchstat produced no rows at all - both sides failed to produce " +
			"benchmark output. See the workflow logs.")
		if *emitGoBench != "" {
			os.WriteFile(*emitGoBench, nil, 0o644)
		}
		writeAll(0)
		return
	}

	add("`%s` (base) vs `%s` (head), interleaved on one runner.", *baseLabel, *headLabel)
	add("")

	var regressions, improvements []finding
	added, removed := map[string]bool{}, map[string]bool{}
	medians := map[string]map[string]float64{}
	var medianOrder []string // preserve first-seen order for stable output

	for _, sec := range sections {
		th, ok := thresholds[sec.unit]
		if !ok {
			th = defaultThreshold
		}
		add("### %s", sec.unit)
		add("")
		add("| benchmark | base | head | change | |")
		add("|---|---:|---:|---:|---|")

		for _, r := range sec.rows {
			if r.name != "geomean" && r.new != nil {
				if _, seen := medians[r.name]; !seen {
					medians[r.name] = map[string]float64{}
					medianOrder = append(medianOrder, r.name)
				}
				medians[r.name][sec.unit] = *r.new
			}

			label := "`" + r.name + "`"
			if r.name == "geomean" {
				label = "**geomean**"
			}

			// A benchmark present on only one side has nothing to compare
			// against; record it as added or removed and move on.
			if r.old == nil || r.new == nil {
				note := "new"
				if r.new == nil {
					note = "removed"
				}
				if r.name != "geomean" {
					if r.old == nil {
						added[r.name] = true
					} else {
						removed[r.name] = true
					}
				}
				add("| %s | %s | %s | - | %s |", label, humanize(sec.unit, r.old), humanize(sec.unit, r.new), note)
				continue
			}

			delta := pct(*r.old, *r.new)
			significant := r.vs != "~"
			flag := ""
			if r.name != "geomean" && math.Abs(delta) > th.pct && (significant || !th.needsSignificance) {
				f := finding{sec.unit, r.name, *r.old, *r.new, delta, r.p}
				if delta > 0 {
					flag = "🔴"
					regressions = append(regressions, f)
				} else {
					flag = "🟢"
					improvements = append(improvements, f)
				}
			} else if !significant && th.needsSignificance {
				flag = "~"
			}
			add("| %s | %s | %s | %s | %s |", label, humanize(sec.unit, r.old), humanize(sec.unit, r.new), fmtPct(delta), flag)
		}
		add("")
	}

	add("### Verdict")
	add("")
	if len(regressions) > 0 {
		add("**%d regression(s)** past threshold (sec/op >%.0f%% and significant, "+
			"allocs/op and B/op >%.0f%%):", len(regressions),
			thresholds["sec/op"].pct, thresholds["allocs/op"].pct)
		add("")
		for _, f := range regressions {
			add("- `%s` %s: %s → %s (%s) %s", f.name, f.unit,
				humanize(f.unit, &f.old), humanize(f.unit, &f.new), fmtPct(f.delta), f.p)
		}
	} else {
		add("No regressions past threshold. ✅")
	}
	if len(added) > 0 || len(removed) > 0 {
		add("")
		if len(added) > 0 {
			add("Benchmarks with no baseline (new here): %s", quoteJoin(sortedKeys(added)))
		}
		if len(removed) > 0 {
			add("Benchmarks present on base but not head (renamed or removed): %s", quoteJoin(sortedKeys(removed)))
		}
	}
	if len(improvements) > 0 {
		add("")
		add("Improvements:")
		add("")
		for _, f := range improvements {
			add("- `%s` %s: %s → %s (%s) %s", f.name, f.unit,
				humanize(f.unit, &f.old), humanize(f.unit, &f.new), fmtPct(f.delta), f.p)
		}
	}
	add("")
	add("<sub>`~` means benchstat could not distinguish the difference from noise. " +
		"Wall-clock numbers are only comparable within this run; allocation counts " +
		"are deterministic and comparable everywhere.</sub>")

	if *emitGoBench != "" {
		// Synthesise a canonical `go test -bench` output from the head medians so
		// the gh-pages time series records a stable median instead of one noisy
		// sample. benchstat strips the "Benchmark" prefix; put it back.
		var b strings.Builder
		b.WriteString("goos: linux\ngoarch: amd64\npkg: " + *pkg + "\n")
		for _, name := range medianOrder {
			full := name
			if !strings.HasPrefix(full, "Benchmark") {
				full = "Benchmark" + full
			}
			b.WriteString(full + "\t1")
			if v, ok := medians[name]["sec/op"]; ok {
				fmt.Fprintf(&b, "\t%.2f ns/op", v*1e9)
			}
			if v, ok := medians[name]["B/op"]; ok {
				fmt.Fprintf(&b, "\t%.0f B/op", v)
			}
			if v, ok := medians[name]["allocs/op"]; ok {
				fmt.Fprintf(&b, "\t%.0f allocs/op", v)
			}
			b.WriteString("\n")
		}
		b.WriteString("PASS\n")
		if err := os.WriteFile(*emitGoBench, []byte(b.String()), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "bench-report:", err)
			os.Exit(2)
		}
	}

	writeAll(len(regressions))
	if len(regressions) > 0 && *failOnReg {
		os.Exit(1)
	}
}
