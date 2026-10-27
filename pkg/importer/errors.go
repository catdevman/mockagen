package importer

import "errors"

// Sentinel errors Compile and the codecs return. Callers match them with
// errors.Is. The text is the whole message a person sees for that fault;
// column names are added by Diagnostic, not baked into the sentinel.
var (
	// ErrUnknownKind means the file's kind is not FieldMap. A Spec or a
	// Schema handed to this loader would otherwise decode halfway and fail
	// later, on a missing target, looking like a bad map.
	ErrUnknownKind = errors.New("importer: unknown kind")

	// ErrUnsupportedFormat means the extension is not YAML or JSON. TOML is
	// excluded for the same reason the schema files exclude it: a list of
	// single-key maps is a bad TOML shape.
	ErrUnsupportedFormat = errors.New("importer: unsupported file format")

	// ErrUnknownTransform means a chain step is not one of the six
	// operations. expr is separate: it is a reserved key, not a typo.
	ErrUnknownTransform = errors.New("importer: unknown transform")

	// ErrExprReserved means the chain contains the expr key. It is
	// documented so a file can name it, and rejected so a file cannot run it.
	ErrExprReserved = errors.New("importer: expr is reserved")

	// ErrBadRegex means a regex_replace pattern did not compile. The pattern
	// is fixed for the whole file, so this is a map error, not a row error.
	ErrBadRegex = errors.New("importer: invalid regular expression")

	// ErrChainKind means a step's input kind is not the previous step's
	// output kind. Every shipped transform is text-to-text; the check exists
	// so a later transform cannot be added without a compile failure.
	ErrChainKind = errors.New("importer: transform chain kinds do not line up")

	// ErrTwoOrigins means a field set more than one of source, sources,
	// coalesce, and constant. Which value would win is not defined.
	ErrTwoOrigins = errors.New("importer: field has more than one origin")

	// ErrSourcesNeedJoin means sources was set without join. Several sources
	// used to be read as coalesce; that silently changed which value landed
	// in the column, so it is now a compile error.
	ErrSourcesNeedJoin = errors.New("importer: sources require join")

	// ErrNoOrigin means the field names a target and no value to put there.
	ErrNoOrigin = errors.New("importer: field has no origin")

	// ErrFormatNotTime means format was set on a column whose sql_type is
	// not a time. format is a parse layout, so on a text column it would
	// quietly do nothing or reject every row.
	ErrFormatNotTime = errors.New("importer: format is only valid on a time column")

	// ErrBadConstant means a constant does not survive the chain and the
	// column's type. It is the same value on every row, so import must not
	// discover it on row 1.
	ErrBadConstant = errors.New("importer: constant cannot be coerced")

	// ErrIdentityTarget means a field writes a column the database fills.
	// Postgres rejects an explicit value for GENERATED ALWAYS, and a bulk
	// load maps fields by position, so the column has to stay off the list.
	ErrIdentityTarget = errors.New("importer: identity column cannot be mapped")

	// ErrBlocker means a NOT NULL column with no default and no identity has
	// no field. The load would reject every row, and the map can say so now.
	ErrBlocker = errors.New("importer: required column is not mapped")

	// ErrUnseen means a column is neither mapped nor omitted. A nullable or
	// defaulted column left in that state was never acknowledged, so a
	// schema refresh would be indistinguishable from a forgotten column.
	ErrUnseen = errors.New("importer: column is neither mapped nor omitted")

	// ErrStale means a field or an omitted name is not on the schema. The
	// usual cause is a rename: the old entry has to stay visible so it is
	// not deleted under the person who wrote it.
	ErrStale = errors.New("importer: name is not in the schema")

	// ErrUnfinishedLookup means a map transform still carries the seeded
	// `to` list and no incoming keys. Import would otherwise copy source
	// values that are not the enum's labels and fail the check per row.
	ErrUnfinishedLookup = errors.New("importer: lookup has no incoming keys")

	// ErrBadOmitted means omitted names a column that is NOT NULL, has no
	// default, and is not identity. Omitting it would drop a required value.
	ErrBadOmitted = errors.New("importer: column cannot be omitted")

	// ErrDuplicateTarget means two fields write the same column. The file
	// does not say which one wins.
	ErrDuplicateTarget = errors.New("importer: duplicate target")

	// ErrUnknownSource means a field reads a name that is not in the pinned
	// header or index list. A typo here shifts no columns; it fails the map.
	ErrUnknownSource = errors.New("importer: source is not in the header")

	// ErrUnknownCase means case is not upper, lower, or title.
	ErrUnknownCase = errors.New("importer: unknown case")

	// ErrBadPad means a pad width is below 1 or its fill string is empty.
	// An empty fill would loop forever trying to reach the width.
	ErrBadPad = errors.New("importer: invalid pad")

	// ErrEmptyReplace means replace-from is empty. ReplaceAll would not
	// advance, so this is rejected before any row.
	ErrEmptyReplace = errors.New("importer: empty replace from")

	// ErrLookupMiss means a finished lookup had no pair for the value and
	// no default. It is a row error: the set of incoming codes is data.
	ErrLookupMiss = errors.New("importer: lookup miss")
)
