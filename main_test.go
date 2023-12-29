package main

import (
	"fmt"
	"testing"
)

func BenchmarkGenerateFakes(b *testing.B) {
    inputs := []int{1, 10, 100, 1000, 10000, 100000, 1000000}
	config := LoadConfig("./test_data/config/single.schema.json")
    for size := range inputs {
        b.Run(fmt.Sprintf("generate_fake_%d", size), func(b *testing.B) {
            config.NumberOfRecords = size
            for i := 0; i < b.N; i++ {
                generateFakes(config)
            }
        })
    }
}
