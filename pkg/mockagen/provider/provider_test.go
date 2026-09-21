package provider

import (
	"slices"
	"strings"
	"testing"
)

// checkSentence asserts the shape every generated sentence must have:
// capitalised first word, full stop, and every word drawn from loremWords.
func checkSentence(t *testing.T, s string) {
	t.Helper()
	if s == "" {
		t.Fatal("empty sentence")
	}
	if !strings.HasSuffix(s, ".") {
		t.Errorf("sentence %q does not end in a full stop", s)
	}
	if c := s[0]; c < 'A' || c > 'Z' {
		t.Errorf("sentence %q does not start with a capital", s)
	}
	words := strings.Fields(strings.TrimSuffix(s, "."))
	if len(words) < 4 {
		t.Errorf("sentence %q has only %d words", s, len(words))
	}
	for i, w := range words {
		if i == 0 {
			w = strings.ToLower(w[:1]) + w[1:]
		}
		if !slices.Contains(loremWords, w) {
			t.Errorf("sentence %q contains %q, which is not in loremWords", s, w)
		}
	}
}

func TestSentence(t *testing.T) {
	for range 200 {
		checkSentence(t, sentence())
	}
}

func TestParagraph(t *testing.T) {
	for range 100 {
		p := paragraph()
		sentences := strings.SplitAfter(p, ". ")
		if len(sentences) < 3 {
			t.Fatalf("paragraph %q has only %d sentences", p, len(sentences))
		}
		for _, s := range sentences {
			checkSentence(t, strings.TrimSpace(s))
		}
	}
}

func TestWord(t *testing.T) {
	seen := map[string]bool{}
	for range 500 {
		w := word()
		if !slices.Contains(loremWords, w) {
			t.Fatalf("word() returned %q, which is not in loremWords", w)
		}
		seen[w] = true
	}
	// A generator that always returned the same word would pass the check
	// above; 500 draws from a list this size should hit many distinct words.
	if len(seen) < 20 {
		t.Errorf("word() produced only %d distinct values in 500 draws", len(seen))
	}
}

// TestTypeMapRegistered guards the wiring: every tag TypeMap points at must
// actually be registered with faker, or the column silently generates a
// random string instead of the requested type.
func TestTypeMapRegistered(t *testing.T) {
	for typeName, tag := range TypeMap {
		if !strings.HasPrefix(tag, "mockagen_") {
			t.Errorf("type %q maps to %q, which is not one of this package's tags", typeName, tag)
		}
		if !registered[tag] {
			t.Errorf("type %q maps to tag %q, which init() never registers", typeName, tag)
		}
	}
}
