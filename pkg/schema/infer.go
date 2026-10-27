package schema

import (
	"slices"
	"strings"
)

// userInferred is the inferred_by literal a hand edit carries. Every other
// value is a rule id, and a second run replaces it. Without this one
// spelling the file cannot tell a decision a person made from one a rule
// made.
const userInferred = "user"

// inferenceRule is one row of the design's inference table, applied in
// slice order: key, then enum, then name, then type. An empty typ means
// the rule matches and the column stays unresolved.
//
// key:int-pk and type:numeric name types the catalog does not have, a
// row-number type and Number. A catalog entry would not make either one
// generate: faker takes no per-column parameters, and generateFakes
// builds every field as a string. Emitting the name fails Render with
// ErrUnknownType or, past that, misses mapToFaker and comes back as
// random text. The column stays unresolved instead.
type inferenceRule struct {
	id     string
	typ    string
	match  func(Column) bool
	values func(Column) []string
}

var inferenceRules = []inferenceRule{
	{
		id: "key:int-pk",
		match: func(c Column) bool {
			return c.PrimaryKey && isIntegerType(c.SQLType)
		},
	},
	{
		// A uuid primary key is this rule in the design's own example.
		// type:uuid is the later rule, for a uuid that is not a key.
		id:  "key:text-pk",
		typ: "GUID",
		match: func(c Column) bool {
			return (c.PrimaryKey || c.Unique) && isTextKey(c.SQLType)
		},
	},
	{
		id:  "rule:enum",
		typ: "Custom List",
		match: func(c Column) bool {
			return len(c.EnumValues) > 0
		},
		values: func(c Column) []string {
			return slices.Clone(c.EnumValues)
		},
	},
	{
		id:  "name:email",
		typ: "Email Address",
		match: func(c Column) bool {
			return emailName(c.Name)
		},
	},
	{
		id:  "name:first_name",
		typ: "First Name",
		match: func(c Column) bool {
			return firstName(c.Name)
		},
	},
	{
		id:  "name:*_at",
		typ: "Datetime",
		match: func(c Column) bool {
			return atName(c.Name)
		},
	},
	{
		id:  "type:uuid",
		typ: "GUID",
		match: func(c Column) bool {
			return isUUIDType(c.SQLType)
		},
	},
	{
		id:  "type:bool",
		typ: "Boolean",
		match: func(c Column) bool {
			return isBoolType(c.SQLType)
		},
	},
	{
		id:  "type:timestamp",
		typ: "Datetime",
		match: func(c Column) bool {
			return isTimestampType(c.SQLType)
		},
	},
	{
		// decimal is mysql and mssql's spelling of numeric.
		// float and real are not this rule.
		id: "type:numeric",
		match: func(c Column) bool {
			return isNumericType(c.SQLType)
		},
	},
	{
		id:  "type:text",
		typ: "Sentence",
		match: func(c Column) bool {
			return isTextType(c.SQLType)
		},
	},
}

// Infer fills generator decisions from column facts. A column marked
// inferred_by user is left alone, which is how a second run keeps a hand
// edit. Every other column is decided again: a fact that changed must not
// keep the previous guess.
//
// json and jsonb match nothing, including the name rules. A jsonb column
// called email is a document, not an email address. jsonb[] is an array
// and is not that exemption. nvarchar, including the nvarchar(max) SQL
// Server 2022 uses as a JSON bag, still matches.
func Infer(s Schema) Schema {
	out := s
	out.Columns = slices.Clone(s.Columns)
	for i, c := range out.Columns {
		if c.InferredBy == userInferred {
			// Clone so a later edit of the decision cannot
			// change the caller's slice.
			c.Values = slices.Clone(c.Values)
			out.Columns[i] = c
			continue
		}
		c.Type = ""
		c.Values = nil
		c.InferredBy = ""
		if !isJSONDocument(c.SQLType) {
			for _, r := range inferenceRules {
				if !r.match(c) {
					continue
				}
				// Empty typ still matches. Falling through
				// would name an integer key by its column,
				// which is the guess the gap refuses.
				if r.typ == "" {
					break
				}
				c.Type = r.typ
				c.InferredBy = r.id
				if r.values != nil {
					c.Values = r.values(c)
				}
				break
			}
		}
		out.Columns[i] = c
	}
	return out
}

// sqlBase strips a length and collapses whitespace. The four catalogs
// disagree on case and on a space before the parenthesis, and the rules
// match the type, not that spelling.
func sqlBase(sqlType string) string {
	t := strings.ToLower(strings.TrimSpace(sqlType))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	return strings.Join(strings.Fields(t), " ")
}

// isIntegerType recognises the integer spellings the four catalogs
// return. numeric is absent on purpose, including numeric(p,0): that is
// the type:numeric rule, and a scale of 0 does not make it a row number.
func isIntegerType(sqlType string) bool {
	switch sqlBase(sqlType) {
	case "smallint", "integer", "int", "bigint", "tinyint", "mediumint",
		"serial", "bigserial", "smallserial", "int2", "int4", "int8",
		"serial2", "serial4", "serial8":
		return true
	default:
		return false
	}
}

// isTextType is the type:text family, including the spellings
// format_type actually returns. Postgres says "character varying", not
// varchar. nchar is mssql's fixed-width national char, the pair of
// nvarchar the way char is the pair of varchar.
func isTextType(sqlType string) bool {
	switch sqlBase(sqlType) {
	case "text", "varchar", "char", "nvarchar", "nchar",
		"character", "character varying":
		return true
	default:
		return false
	}
}

// isTextKey is a text column or a uuid. The design's uuid primary key is
// inferred as key:text-pk, so a uuid key is a text key even though
// type:uuid is a later rule for a uuid that is not one.
func isTextKey(sqlType string) bool {
	if isTextType(sqlType) {
		return true
	}
	return isUUIDType(sqlType)
}

// isUUIDType includes uniqueidentifier, which is the spelling SQL
// Server stores for a uuid. type:uuid is one rule across both.
func isUUIDType(sqlType string) bool {
	switch sqlBase(sqlType) {
	case "uuid", "uniqueidentifier":
		return true
	default:
		return false
	}
}

// isBoolType treats tinyint(1) as boolean and leaves every other tinyint
// alone. bit varying is a bit string, not a flag, so the base has to be
// exactly bit.
func isBoolType(sqlType string) bool {
	flat := strings.ToLower(strings.Join(strings.Fields(sqlType), ""))
	if flat == "tinyint(1)" {
		return true
	}
	switch sqlBase(sqlType) {
	case "boolean", "bool", "bit":
		return true
	default:
		return false
	}
}

// isTimestampType matches the type:timestamp family. datetime2 and
// datetimeoffset are SQL Server's datetime. A bare time is not in the
// rule, and a prefix match on "time" would steal timestamp's name only
// to include it, so date is exact.
func isTimestampType(sqlType string) bool {
	base := sqlBase(sqlType)
	switch {
	case base == "date":
		return true
	case strings.HasPrefix(base, "timestamp"), strings.HasPrefix(base, "datetime"):
		return true
	default:
		return false
	}
}

// isNumericType is numeric(p,s) and the dialect spellings of it. float
// and real are not here; they match no rule.
func isNumericType(sqlType string) bool {
	switch sqlBase(sqlType) {
	case "numeric", "decimal", "number", "dec":
		return true
	default:
		return false
	}
}

// isJSONDocument is exact. jsonb[] shares a prefix with jsonb and is an
// array, not a document; a prefix match would hide that column from the
// name rules the design still allows to fire.
func isJSONDocument(sqlType string) bool {
	switch sqlBase(sqlType) {
	case "json", "jsonb":
		return true
	default:
		return false
	}
}

// emailName takes email, email_address, and a *_email suffix.
// email_address does not end in _email, so it is matched on its own;
// notemail matches neither form.
func emailName(name string) bool {
	n := normName(name)
	return n == "email" || n == "email_address" || strings.HasSuffix(n, "_email")
}

func firstName(name string) bool {
	switch normName(name) {
	case "first_name", "fname", "given_name":
		return true
	default:
		return false
	}
}

// atName requires a prefix. The rule is *_at, and a column named "_at"
// or "at" is not a timestamp someone named.
func atName(name string) bool {
	n := normName(name)
	return len(n) > len("_at") && strings.HasSuffix(n, "_at")
}

func normName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
