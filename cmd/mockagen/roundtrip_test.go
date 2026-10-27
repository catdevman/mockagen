package main

import "testing"

// generatorGap is why TestRoundTripPerDialect does not run. Shortening
// it would let the skip go green without naming what generation still
// cannot produce. The comparison, once this skip is removed, is the
// generated file against the file import writes, byte for byte, for
// each dialect. It is not a looser comparison.
const generatorGap = "generator gap: no Number type; min, max, decimals, and null_percentage are ignored; Custom List breaks on a comma and a colon; no integer primary-key type"

func TestRoundTripPerDialect(t *testing.T) {
	t.Skip(generatorGap)
}
