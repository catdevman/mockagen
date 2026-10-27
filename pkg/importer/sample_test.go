package importer_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
)

func TestHeaderMatches(t *testing.T) {
	// A swap has to fail the check. Propose would otherwise retarget
	// by name and a reordered file would look like a clean re-run.
	spec := importer.SourceSpec{HasHeader: true, Fields: []string{"email", "note"}}
	same := importer.Sample{HasHeader: true, Fields: []string{"email", "note"}}
	swap := importer.Sample{HasHeader: true, Fields: []string{"note", "email"}}
	if !importer.HeaderMatches(spec, same) {
		t.Fatal("identical header did not match")
	}
	if importer.HeaderMatches(spec, swap) {
		t.Fatal("swapped header matched")
	}
}

func TestReadSampleKeepsPreview(t *testing.T) {
	// The csv reader reuses its record slice. The preview has to be a
	// copy, or the mapping screen would show the last row in every slot.
	dir := t.TempDir()
	path := filepath.Join(dir, "rows.csv")
	body := "email,note\nada@example.com,legacy\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := importer.ReadSample(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rows != 1 || len(got.Preview) != 1 {
		t.Fatalf("rows = %d preview = %d", got.Rows, len(got.Preview))
	}
	want := []string{"ada@example.com", "legacy"}
	if !slices.Equal(got.Preview[0], want) {
		t.Fatalf("preview = %q, want %q", got.Preview[0], want)
	}
}

func TestReadSampleDuplicateHeader(t *testing.T) {
	// Two columns with one name cannot both be pinned. Propose matches
	// by that name, so the second would be invisible and still loaded.
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.csv")
	if err := os.WriteFile(path, []byte("a,a\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := importer.ReadSample(path, 1)
	if err == nil || !strings.Contains(err.Error(), `duplicate header "a"`) {
		t.Fatalf("err = %v", err)
	}
}
