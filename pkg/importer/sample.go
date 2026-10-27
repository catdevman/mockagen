package importer

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
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
	// csv reader's reused buffer. The mapping screen shows them beside the
	// chain. A second pass could observe a file that changed underneath,
	// and the cap is the same sample limit either way.
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
	f, err := os.Open(path)
	if err != nil {
		return Sample{}, err
	}
	defer f.Close()
	return readSample(f, limit)
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
