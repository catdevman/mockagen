package importer

import (
	"errors"
	"testing"
)

// TestAlignStep pins the kind check Compile runs. No shipped transform
// leaves text, so a field map cannot produce ErrChainKind yet; this is the
// comparison that would.
func TestAlignStep(t *testing.T) {
	if err := alignStep(stepText, stepOther, "later"); !errors.Is(err, ErrChainKind) {
		t.Fatalf("err = %v", err)
	}
	if err := chainKinds([]Transform{{Name: "trim"}, {Name: "case"}}); err != nil {
		t.Fatal(err)
	}
}
