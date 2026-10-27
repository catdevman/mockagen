package importer

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/catdevman/mockagen/pkg/schema"
)

// fuzzyMax is the furthest a typo may be and still count as a match. Two
// edits covers a transposition. Anything further starts gluing an unrelated
// leftover column to whatever source is still free.
const fuzzyMax = 2

// Propose builds a field map from sch and the source sample. base, when
// set, keeps every field entry and the omitted list as they were written:
// a re-run fills only columns that are in neither, and does not put trim
// back onto a chain a person already edited.
//
// Identity columns are omitted even when a source shares their name. Postgres
// rejects an explicit value for GENERATED ALWAYS, so a name match is not a
// reason to write the column. An unmatched nullable column, and an unmatched
// column with a database default, are omitted rather than left unseen, so a
// fresh map is clean apart from real blockers.
func Propose(sch schema.Schema, sample Sample, base *FieldMap) FieldMap {
	out := FieldMap{
		Version: "mockagen/v1",
		Kind:    "FieldMap",
		Name:    sch.Name + "-import",
		Source: SourceSpec{
			Format:    sample.Format,
			HasHeader: sample.HasHeader,
			Delimiter: sample.Delimiter,
			Encoding:  sample.Encoding,
			Fields:    append([]string(nil), sample.Fields...),
			Indexes:   append([]int(nil), sample.Indexes...),
		},
		Unmapped: UnmappedPolicy{Source: "ignore"},
		OnError:  "abort",
	}
	if sch.Name == "" {
		out.Name = "import"
	}
	if base != nil {
		if base.Version != "" {
			out.Version = base.Version
		}
		if base.Name != "" {
			out.Name = base.Name
		}
		if base.Unmapped.Source != "" {
			out.Unmapped = base.Unmapped
		}
		if base.OnError != "" {
			out.OnError = base.OnError
		}
		out.Fields = append([]Field(nil), base.Fields...)
		out.Omitted = append([]string(nil), base.Omitted...)
		// The sample carries names, not positions. Fixed is the only
		// copy of the layout; dropping it makes the next open fail.
		if len(base.Source.Fixed) > 0 {
			out.Source.Fixed = append([]FixedCol(nil), base.Source.Fixed...)
		}
	}

	taken := map[string]bool{}
	for _, f := range out.Fields {
		taken[f.Target] = true
	}
	for _, name := range out.Omitted {
		taken[name] = true
	}

	var pending []schema.Column
	for _, col := range sch.Columns {
		if taken[col.Name] {
			continue
		}
		if col.Identity {
			out.Omitted = append(out.Omitted, col.Name)
			taken[col.Name] = true
			continue
		}
		pending = append(pending, col)
	}

	sources := sample.Fields
	if !sample.HasHeader {
		sources = indexNames(sample.Indexes)
	}
	matched := matchColumns(pending, sources)
	var omitRest []schema.Column
	for _, col := range pending {
		src, ok := matched[col.Name]
		if !ok {
			omitRest = append(omitRest, col)
			continue
		}
		out.Fields = append(out.Fields, Field{
			Target:     col.Name,
			Source:     src,
			Transforms: seedTransforms(col),
		})
	}
	for _, col := range omitRest {
		if col.Nullable || col.Default != "" {
			out.Omitted = append(out.Omitted, col.Name)
		}
	}
	return out
}

func indexNames(idxs []int) []string {
	names := make([]string, len(idxs))
	for i, n := range idxs {
		names[i] = strconv.Itoa(n)
	}
	return names
}

// SeedTransforms is the chain a fresh link starts with. Propose and the
// mapping screen both call it, so a hand-linked column and an automatic
// match do not begin from different chains.
func SeedTransforms(col schema.Column) []Transform {
	return seedTransforms(col)
}

func seedTransforms(col schema.Column) []Transform {
	if len(col.EnumValues) > 0 {
		return []Transform{
			{Name: "trim"},
			{Name: "map", Accept: append([]string(nil), col.EnumValues...)},
		}
	}
	if classify(col.SQLType, col.NumericScale) == KindText {
		return []Transform{{Name: "trim"}}
	}
	return nil
}

// matchColumns assigns each column at most one source. Passes run in order
// and stop at the first pass that yields a single winner: exact name, then
// normalised name, then a fuzzy match. A source may feed two columns when
// each pass finds it unambiguously; an ambiguous pair is left unmatched so
// the blocker stays visible instead of being papered over.
func matchColumns(cols []schema.Column, sources []string) map[string]string {
	left := append([]schema.Column(nil), cols...)
	out := map[string]string{}
	take := func(pairs map[string]string) {
		for col, src := range pairs {
			out[col] = src
		}
		next := left[:0]
		for _, c := range left {
			if _, ok := pairs[c.Name]; !ok {
				next = append(next, c)
			}
		}
		left = next
	}
	take(matchExact(left, sources))
	take(matchNormalised(left, sources))
	take(matchFuzzy(left, sources))
	return out
}

func matchExact(cols []schema.Column, sources []string) map[string]string {
	have := map[string]int{}
	for _, src := range sources {
		have[src]++
	}
	out := map[string]string{}
	for _, c := range cols {
		if have[c.Name] == 1 {
			out[c.Name] = c.Name
		}
	}
	return out
}

func matchNormalised(cols []schema.Column, sources []string) map[string]string {
	type hit struct {
		name string
		n    int
	}
	srcHits := map[string]hit{}
	for _, src := range sources {
		key := normalise(src)
		h := srcHits[key]
		h.n++
		if h.n == 1 {
			h.name = src
		}
		srcHits[key] = h
	}
	colHits := map[string]int{}
	for _, c := range cols {
		colHits[normalise(c.Name)]++
	}
	out := map[string]string{}
	for _, c := range cols {
		key := normalise(c.Name)
		sh := srcHits[key]
		if sh.n == 1 && colHits[key] == 1 {
			out[c.Name] = sh.name
		}
	}
	return out
}

func matchFuzzy(cols []schema.Column, sources []string) map[string]string {
	type cand struct {
		col  string
		src  string
		dist int
	}
	var pairs []cand
	for _, src := range sources {
		ns := normalise(src)
		best, second := -1, -1
		bestCol := ""
		for _, c := range cols {
			d := levenshtein(ns, normalise(c.Name))
			if best < 0 || d < best {
				second = best
				best = d
				bestCol = c.Name
			} else if second < 0 || d < second {
				second = d
			}
		}
		if best < 0 || best > fuzzyMax {
			continue
		}
		if second >= 0 && best >= second {
			continue
		}
		pairs = append(pairs, cand{bestCol, src, best})
	}
	byCol := map[string][]cand{}
	for _, p := range pairs {
		byCol[p.col] = append(byCol[p.col], p)
	}
	out := map[string]string{}
	for col, cs := range byCol {
		best := cs[0]
		amb := false
		for _, c := range cs[1:] {
			if c.dist < best.dist {
				best = c
				amb = false
			} else if c.dist == best.dist {
				amb = true
			}
		}
		if !amb {
			out[col] = best.src
		}
	}
	return out
}

// normalise folds case, turns spaces and hyphens into underscores, drops
// other punctuation, and collapses underscore runs. The collapse is what
// makes "email - address" meet "email_address"; a doubled separator would
// otherwise miss on the second pass and fall through to a fuzzier one.
func normalise(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case r == ' ' || r == '-':
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			if r == '_' && prevUnderscore {
				continue
			}
			b.WriteRune(r)
			prevUnderscore = r == '_'
		default:
			prevUnderscore = false
		}
	}
	return strings.Trim(b.String(), "_")
}

func levenshtein(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = min(del, ins, sub)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}
