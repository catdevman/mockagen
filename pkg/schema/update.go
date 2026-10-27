package schema

import (
	"fmt"
	"slices"
)

// Update refreshes facts from fresh and keeps decisions marked user.
// fresh is a new introspection; its decisions are ignored, because they
// were not in the file. Columns match by name. A rename is a drop plus
// an add, which is what the drift lines say: guessing that two columns
// are the same one would hide the rename.
//
// A decision not marked user is not copied. The marker is the only thing
// that distinguishes a hand edit from a previous guess, and the guess is
// not kept.
func Update(prev, fresh Schema) (Schema, []string) {
	prevByName := make(map[string]Column, len(prev.Columns))
	for _, c := range prev.Columns {
		prevByName[c.Name] = c
	}
	out := fresh
	out.Columns = make([]Column, len(fresh.Columns))
	seen := make(map[string]struct{}, len(fresh.Columns))
	var drift []string
	for i, c := range fresh.Columns {
		seen[c.Name] = struct{}{}
		old, ok := prevByName[c.Name]
		if !ok {
			drift = append(drift, fmt.Sprintf("column %q added", c.Name))
		} else if old.SQLType != c.SQLType {
			drift = append(drift, fmt.Sprintf(
				"column %q sql_type %q is now %q", c.Name, old.SQLType, c.SQLType))
		}
		if ok && old.InferredBy == userInferred {
			c = keepUserDecisions(c, old)
		}
		out.Columns[i] = c
	}
	for _, old := range prev.Columns {
		if _, ok := seen[old.Name]; !ok {
			drift = append(drift, fmt.Sprintf("column %q dropped", old.Name))
		}
	}
	return Infer(out), drift
}

// keepUserDecisions copies the decision fields onto a column that already
// holds the new facts. Copying the whole struct would put the old
// sql_type back, which is the field -update exists to refresh.
func keepUserDecisions(fresh, prev Column) Column {
	fresh.Type = prev.Type
	fresh.Values = slices.Clone(prev.Values)
	fresh.InferredBy = prev.InferredBy
	fresh.NullPercentage = prev.NullPercentage
	fresh.Formula = prev.Formula
	fresh.Min = prev.Min
	fresh.Max = prev.Max
	fresh.Format = prev.Format
	fresh.Blank = prev.Blank
	fresh.Decimals = prev.Decimals
	fresh.SelectionStyle = prev.SelectionStyle
	fresh.Distribution = prev.Distribution
	fresh.StartPosition = prev.StartPosition
	fresh.EndPosition = prev.EndPosition
	fresh.Alignment = prev.Alignment
	fresh.PadCharacter = prev.PadCharacter
	return fresh
}
