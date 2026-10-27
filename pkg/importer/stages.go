package importer

import "github.com/catdevman/mockagen/pkg/schema"

// Stage is one step of a field, in the order Apply runs them. The editor
// shows these beside the sample. Value is the text after this step. Null
// means the step produced NULL. Err is set on the step that failed; later
// steps are omitted so the failure stays on the step that caused it.
type Stage struct {
	Name  string
	Value string
	Null  bool
	Err   error
}

// Stages runs the same steps as Apply and returns each one. It exists so
// the mapping screen can show null_if, coercion, default, and the schema
// checks without a second copy of the pipeline.
func Stages(col schema.Column, f Field, inputs []string) []Stage {
	if originOf(f) == originCoalesce {
		return stagesCoalesce(col, f, inputs)
	}
	raw, err := reduce(f, inputs)
	if err != nil {
		return []Stage{{Name: "chain", Err: err}}
	}
	cur, err := applyChain(f.Transforms, raw)
	if err != nil {
		return []Stage{{Name: "chain", Err: err}}
	}
	return stagesAfter(col, f, cur, false)
}

// stagesCoalesce walks candidates the way firstCoalesce does, then shows
// one chain and one null_if for the candidate that survived. Drawing
// every candidate would be a second pipeline, and a later chain error
// must not be hidden behind an earlier null.
func stagesCoalesce(col schema.Column, f Field, inputs []string) []Stage {
	if len(inputs) != len(f.Coalesce) {
		_, _, err := firstCoalesce(f, inputs)
		return []Stage{{Name: "chain", Err: err}}
	}
	var last string
	for _, in := range inputs {
		cur, err := applyChain(f.Transforms, in)
		if err != nil {
			return []Stage{{Name: "chain", Err: err}}
		}
		last = cur
		if !listed(cur, f.NullIf) {
			return stagesAfter(col, f, cur, false)
		}
	}
	// None survived. last is the text null_if rejected, so the chain
	// step is not a blank that looks like the cell was empty.
	return stagesAfter(col, f, last, true)
}

// stagesAfter is the tail Apply runs once the chain has a value.
// Coalesce calls it for the candidate that was kept, so null_if
// and the schema checks are not written out a second time.
func stagesAfter(
	col schema.Column, f Field, cur string, alreadyNull bool,
) []Stage {
	stages := []Stage{{Name: "chain", Value: cur}}
	if alreadyNull || listed(cur, f.NullIf) {
		stages = append(stages, Stage{Name: "null_if", Null: true})
		return append(stages, nullStages(col, f)...)
	}
	stages = append(stages, Stage{Name: "null_if", Value: cur})
	kind := classify(col.SQLType, col.NumericScale)
	canon, err := coerce(kind, f.Format, cur)
	if err != nil {
		stages = append(stages, Stage{Name: "coerce", Err: err})
		return stages
	}
	stages = append(stages, Stage{Name: "coerce", Value: canon})
	// The default did not fire. Value repeats the coerced text so the
	// step is visible; it is not the default being applied.
	stages = append(stages, Stage{Name: "default", Value: canon})
	if err := checkFacts(col, kind, canon); err != nil {
		stages = append(stages, Stage{
			Name: "check", Value: canon, Err: err,
		})
		return stages
	}
	stages = append(stages, Stage{Name: "check", Value: canon})
	return stages
}

// nullStages is finishNull split into steps. A nil default stops the
// list: coerce and check would be about a sentinel that was discarded.
// A set default is coerced with an empty format, because format is the
// file's layout and the default is already the column value.
func nullStages(col schema.Column, f Field) []Stage {
	if f.Default == nil {
		return []Stage{{Name: "default", Null: true}}
	}
	kind := classify(col.SQLType, col.NumericScale)
	canon, err := coerce(kind, "", *f.Default)
	if err != nil {
		return []Stage{{Name: "default", Err: err}}
	}
	out := []Stage{{Name: "default", Value: canon}}
	if err := checkFacts(col, kind, canon); err != nil {
		return append(out, Stage{
			Name: "check", Value: canon, Err: err,
		})
	}
	return append(out, Stage{Name: "check", Value: canon})
}
