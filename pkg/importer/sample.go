package importer

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Sample is the bounded look Propose takes at a source file. Rows is how
// many records were read after the header, capped by the caller. The whole
// file is not loaded: a mapping decision that needed the tail of a multi
// gigabyte extract would not finish, and the head is what a wrong delimiter
// shows up in.
type Sample struct {
	Format    string
	Delimiter string
	HasHeader bool
	Encoding  string
	Fields    []string
	Indexes   []int
	Rows      int
	// Preview is the data rows this read already walked, copied out of the
	// source buffer. NULL is "" because Preview is [][]string; the import
	// path still uses OpenSource and keeps the kinds. The mapping screen
	// shows the rows beside the chain. A second pass could observe a file
	// that changed underneath, and the cap is the same sample limit either
	// way.
	Preview [][]string
}

// HeaderMatches reports whether spec is the header sample was read
// with. A renamed or reordered file must not be treated as the same
// source: Propose matches by name, so a swap would look successful
// and write the values into the wrong columns.
func HeaderMatches(spec SourceSpec, sample Sample) bool {
	if spec.HasHeader != sample.HasHeader {
		return false
	}
	if spec.HasHeader {
		return slices.Equal(spec.Fields, sample.Fields)
	}
	return slices.Equal(spec.Indexes, sample.Indexes)
}

// ReadSample reads the header and at most limit data rows from path.
// The delimiter is sniffed from the first physical line, so a quoted newline
// inside the header is not supported; real extracts put the header on one
// line, and a wrong guess here is visible in source.fields.
//
// limit counts data rows, not the header. Zero reads the header only.
func ReadSample(path string, limit int) (Sample, error) {
	return ReadSampleSpec(path, nil, limit)
}

// ReadSampleSpec reads like ReadSample. A spec with Format set is that
// format, because a fixed file's positions live on the map and the
// extension cannot carry them. An empty format follows the extension;
// anything else stays on the CSV sniffer, which is what a .csv file
// with a tab delimiter still needs.
func ReadSampleSpec(path string, spec *SourceSpec, limit int) (Sample, error) {
	format := ""
	if spec != nil {
		format = spec.Format
	}
	if format == "" {
		format = sampleFormat(path)
	}
	if format == "" {
		f, err := os.Open(path)
		if err != nil {
			return Sample{}, err
		}
		defer f.Close()
		return readSample(f, limit)
	}
	return readSampleSource(path, spec, strings.ToLower(format), limit)
}

// sampleFormat is the extension dispatch for a map that has not named a
// format. csv is absent on purpose: a comma file and a tab file share
// the extension, and the sniffer is what tells them apart.
func sampleFormat(path string) string {
	switch strings.ToLower(extOf(path)) {
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".jsonl":
		return "jsonl"
	case ".parquet":
		return "parquet"
	case ".log":
		return "fluent"
	case ".fixed":
		return "fixed"
	case ".tsv":
		return "tsv"
	default:
		return ""
	}
}

// readSampleSource opens path with OpenSource so a fixed layout, a
// parquet kind, and a yaml scalar keep the meanings the sniffer does
// not have. Preview flattens those kinds to strings afterwards.
func readSampleSource(path string, spec *SourceSpec, format string, limit int) (Sample, error) {
	openSpec := SourceSpec{Format: format}
	if spec != nil {
		openSpec = *spec
		openSpec.Format = format
	} else if format == "tsv" {
		// A .tsv name is the header row plus a tab. Leaving HasHeader
		// false would publish the first data row as the column names.
		openSpec.HasHeader = true
	}
	src, err := OpenSource(path, openSpec)
	if err != nil {
		return Sample{}, err
	}
	defer src.Close()

	fields := append([]string(nil), src.Fields()...)
	seen := map[string]bool{}
	for _, name := range fields {
		if seen[name] {
			return Sample{}, fmt.Errorf("importer: duplicate header %q", name)
		}
		seen[name] = true
	}

	hasHeader := true
	if spec != nil {
		hasHeader = spec.HasHeader
	}
	var indexes []int
	if len(fields) == 0 && !hasHeader {
		w, err := widthOf(src)
		if err != nil {
			return Sample{}, err
		}
		indexes = make([]int, w)
		for i := range indexes {
			indexes[i] = i
		}
	}

	rows := 0
	var preview [][]string
	var rec Record
	for rows < limit {
		ok, err := src.Next(&rec)
		if err != nil {
			return Sample{}, err
		}
		if !ok {
			break
		}
		preview = append(preview, previewRow(&rec))
		rows++
	}
	enc := "utf-8"
	if spec != nil && spec.Encoding != "" {
		enc = spec.Encoding
	}
	if !sampleUTF8(fields, preview) {
		// Same reason as the sniffer: recording utf-8 for bytes that
		// are not utf-8 would make import decode them again.
		enc = "unknown"
	}
	return Sample{
		Format:    format,
		Delimiter: sampleDelimiter(format, spec),
		HasHeader: hasHeader,
		Encoding:  enc,
		Fields:    fields,
		Indexes:   indexes,
		Rows:      rows,
		Preview:   preview,
	}, nil
}

// sampleDelimiter is the separator Propose will pin. An empty csv
// delimiter is a comma, which is what openCSV already assumes;
// leaving it blank would drop the key and a later sniff could
// disagree with this read.
func sampleDelimiter(format string, spec *SourceSpec) string {
	if spec != nil && spec.Delimiter != "" {
		return spec.Delimiter
	}
	switch format {
	case "tsv":
		return "\t"
	case "csv":
		return ","
	default:
		return ""
	}
}

// sampleUTF8 reports whether the names and the preview are UTF-8.
// The encoding field is what a later import believes; marking a
// non-UTF-8 sample as utf-8 would decode it a second time.
func sampleUTF8(fields []string, preview [][]string) bool {
	if !utf8.ValidString(strings.Join(fields, "")) {
		return false
	}
	for _, row := range preview {
		if !utf8.ValidString(strings.Join(row, "")) {
			return false
		}
	}
	return true
}

// previewRow copies one record into strings. A number or bool is
// strconv'd, and a KindTime instant is RFC3339Nano, because the screen
// has no kinds. NULL is "" so it fits that grid; the import keeps the
// kind.
func previewRow(r *Record) []string {
	out := make([]string, len(r.Values))
	for i := range r.Values {
		v := r.Values[i]
		switch v.Kind {
		case KindNull:
		case KindInt:
			out[i] = strconv.FormatInt(v.num, 10)
		case KindFloat:
			out[i] = strconv.FormatFloat(v.flt, 'f', -1, 64)
		case KindBool:
			if v.num != 0 {
				out[i] = "true"
			} else {
				out[i] = "false"
			}
		case KindTime:
			out[i] = time.Unix(0, v.num).UTC().Format(time.RFC3339Nano)
		case KindText, KindRaw:
			out[i] = string(r.Bytes(i))
		}
	}
	return out
}

func readSample(r io.Reader, limit int) (Sample, error) {
	br := bufio.NewReader(r)
	line, err := readPhysicalLine(br)
	if err != nil {
		return Sample{}, err
	}
	delim := sniffDelim(string(line))
	csvr := csv.NewReader(io.MultiReader(bytes.NewReader(line), br))
	csvr.Comma = delim
	// A short row in the sample is the same class of bug as a wrong
	// delimiter: the header this map would pin does not describe the rows.
	// FieldsPerRecord stays at the default (match the first record) so that
	// fails here.
	csvr.ReuseRecord = true
	header, err := csvr.Read()
	if err != nil {
		return Sample{}, err
	}
	fields := append([]string(nil), header...)
	if len(fields) > 0 {
		fields[0] = strings.TrimPrefix(fields[0], "\uFEFF")
	}
	seen := map[string]bool{}
	for _, name := range fields {
		if seen[name] {
			return Sample{}, fmt.Errorf("importer: duplicate header %q", name)
		}
		seen[name] = true
	}
	rows := 0
	var preview [][]string
	for rows < limit {
		rec, err := csvr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Sample{}, err
		}
		// ReuseRecord hands the same slice back next time. Copying is
		// what keeps this row from becoming the next one.
		preview = append(preview, append([]string(nil), rec...))
		rows++
	}
	format := "csv"
	if delim == '\t' {
		format = "tsv"
	}
	enc := "utf-8"
	if !utf8.ValidString(strings.Join(fields, "")) {
		// The reader still returns the bytes. Recording utf-8 for a file
		// that is not utf-8 would make import decode it twice and disagree
		// with the header pinned here.
		enc = "unknown"
	}
	return Sample{
		Format:    format,
		Delimiter: string(delim),
		HasHeader: true,
		Encoding:  enc,
		Fields:    fields,
		Rows:      rows,
		Preview:   preview,
	}, nil
}

func readPhysicalLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		frag, err := br.ReadSlice('\n')
		buf = append(buf, frag...)
		if len(buf) > 1<<20 {
			return nil, fmt.Errorf("importer: header line exceeds 1MiB")
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		if err == io.EOF && len(buf) == 0 {
			return nil, io.EOF
		}
		return buf, nil
	}
}

// sniffDelim counts separators outside quotes. A single-column file has none
// and stays comma, which is what encoding/csv already assumes; the count is
// only there so a tab-separated extract is not read as one column.
func sniffDelim(line string) rune {
	var commas, tabs, semis int
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		switch c {
		case ',':
			commas++
		case '\t':
			tabs++
		case ';':
			semis++
		}
	}
	if tabs > commas && tabs >= semis {
		return '\t'
	}
	if semis > commas && semis > tabs {
		return ';'
	}
	return ','
}
