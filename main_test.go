package main

import (
	"testing"
)

func BenchmarkGenerateFakes(b *testing.B) {
	config := LoadConfig("./test_data/config/single.schema.json")
	for i := 0; i < b.N; i++ {
		generateFakes(config)
	}
}
