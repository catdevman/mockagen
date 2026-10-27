package importer

import "strings"

// FileFormats is the file_format list the writer accepts, in the order a
// config names them. sql is the only write-only entry: OpenSource rejects
// it because an INSERT file is not a row source. A new format has to be
// added here and then given a reader or marked with WriteOnlyFormat, so
// the writer cannot grow a name the importer does not know about.
func FileFormats() []string {
	return []string{"csv", "json", "yaml", "fixed", "parquet", "fluent", "sql"}
}

// WriteOnlyFormat reports whether name is a file_format this package does
// not read. sql is the only one. tsv and jsonl are readers, but they are
// not file_format names, so they are not write-only either.
func WriteOnlyFormat(name string) bool {
	return strings.EqualFold(name, "sql")
}
