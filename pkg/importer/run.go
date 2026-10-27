package importer

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrorPolicy is what a bad cell does. ErrorAbort is the zero value: a
// half-loaded table is worse than a failed load, so a map that does not
// say otherwise stops.
type ErrorPolicy int

const (
	ErrorAbort ErrorPolicy = iota
	ErrorSkipRow
	ErrorNullField
)

// Policy parses a field map's on_error. An empty value is abort.
func Policy(s string) (ErrorPolicy, error) {
	switch s {
	case "", "abort":
		return ErrorAbort, nil
	case "skip_row":
		return ErrorSkipRow, nil
	case "null_field":
		return ErrorNullField, nil
	default:
		return 0, fmt.Errorf("importer: unknown on_error %q", s)
	}
}

// Options controls one Run. Limit stops after n source rows. SampleRows
// keeps n mapped rows, copied, because the source reuses its record. The
// map screen's preview is these two plus a discarding sink.
type Options struct {
	OnError    ErrorPolicy
	MaxErrors  int
	Limit      int
	SampleRows int
	// BatchSize is rows per statement. Zero lets the sink decide: the
	// usable size is the column count against the dialect's parameter cap.
	BatchSize int
}

// RowError locates one failure precisely enough to fix the map or the row.
type RowError struct {
	Line   int64
	Target string
	Err    error
}

func (e RowError) Error() string {
	if e.Target == "" {
		return fmt.Sprintf("line %d: %v", e.Line, e.Err)
	}
	return fmt.Sprintf("line %d: column %s: %v", e.Line, e.Target, e.Err)
}

func (e RowError) Unwrap() error { return e.Err }

// Report is the counts from one Run. Samples outlive the row they came
// from; the source reuses its record.
type Report struct {
	Read, Written, Skipped int64
	Errors                 []RowError
	Samples                []Record
	Duration               time.Duration
}

// Run reads src, applies p, and writes dst. A plan with diagnostics must
// not be passed here: Compile's list is the caller's to refuse.
func Run(ctx context.Context, src Source, p *Plan, dst Sink, opts Options) (*Report, error) {
	if p == nil {
		return nil, fmt.Errorf("importer: nil plan")
	}
	start := time.Now()
	rep := &Report{}
	if b, ok := dst.(batchSetter); ok && opts.BatchSize > 0 {
		b.SetBatch(opts.BatchSize)
	}
	if err := dst.Open(ctx, p.Target); err != nil {
		return nil, err
	}
	defer dst.Close(ctx)

	var srcRec, dstRec Record
	for {
		if err := ctx.Err(); err != nil {
			rep.Duration = time.Since(start)
			return rep, err
		}
		if opts.Limit > 0 && int(rep.Read) >= opts.Limit {
			break
		}
		ok, err := src.Next(&srcRec)
		if err != nil {
			rep.Duration = time.Since(start)
			return rep, err
		}
		if !ok {
			break
		}
		rep.Read++
		rowErr, hard := applyRow(p, &srcRec, &dstRec, opts.OnError)
		if hard != nil {
			rep.Errors = append(rep.Errors, RowError{Line: srcRec.Line, Err: hard})
			rep.Duration = time.Since(start)
			return rep, hard
		}
		if rowErr != nil && opts.OnError != ErrorNullField {
			rep.Errors = append(rep.Errors, RowError{
				Line: srcRec.Line, Target: rowErr.target, Err: rowErr.err,
			})
			rep.Skipped++
			if opts.OnError == ErrorAbort || (opts.MaxErrors > 0 && len(rep.Errors) >= opts.MaxErrors) {
				rep.Duration = time.Since(start)
				return rep, rowErr
			}
			continue
		}
		if rowErr != nil {
			// null_field recorded the bad cell as NULL when the column
			// allowed it. A required column falls through as a skip.
			if !nullableCell(p, rowErr.target) {
				rep.Errors = append(rep.Errors, RowError{
					Line: srcRec.Line, Target: rowErr.target, Err: rowErr.err,
				})
				rep.Skipped++
				if opts.MaxErrors > 0 && len(rep.Errors) >= opts.MaxErrors {
					rep.Duration = time.Since(start)
					return rep, rowErr
				}
				continue
			}
			rep.Errors = append(rep.Errors, RowError{
				Line: srcRec.Line, Target: rowErr.target, Err: rowErr.err,
			})
		}
		if err := dst.Write(ctx, &dstRec); err != nil {
			rep.Duration = time.Since(start)
			return rep, err
		}
		rep.Written++
		if opts.SampleRows > 0 && len(rep.Samples) < opts.SampleRows {
			rep.Samples = append(rep.Samples, dstRec.clone())
		}
		if opts.MaxErrors > 0 && len(rep.Errors) >= opts.MaxErrors {
			break
		}
	}
	rep.Duration = time.Since(start)
	return rep, nil
}

func applyRow(p *Plan, src, dst *Record, pol ErrorPolicy) (*fieldError, error) {
	if pol != ErrorNullField {
		err := p.Apply(src, dst)
		if err == nil {
			return nil, nil
		}
		var fe *fieldError
		if errors.As(err, &fe) {
			return fe, nil
		}
		return nil, err
	}
	dst.Reset(len(p.binds))
	dst.Line = src.Line
	var first *fieldError
	for i := range p.binds {
		if err := p.applyOne(i, src, dst); err != nil {
			fe := &fieldError{target: p.binds[i].col.Name, err: err}
			if p.binds[i].col.Nullable || p.binds[i].col.Default != "" {
				dst.SetNull(i)
				if first == nil {
					first = fe
				}
				continue
			}
			return fe, nil
		}
	}
	return first, nil
}

func nullableCell(p *Plan, target string) bool {
	for i := range p.binds {
		if p.binds[i].col.Name == target {
			return p.binds[i].col.Nullable || p.binds[i].col.Default != ""
		}
	}
	return false
}
