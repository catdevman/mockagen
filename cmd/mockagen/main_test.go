package main

import (
	"fmt"
	"testing"

	"github.com/catdevman/mockagen/pkg/mockagen"
)

func BenchmarkGenerateFakes(b *testing.B) {
	inputs := []int{1, 10, 100, 1000, 10000}
	config := mockagen.MockagenConfig{
		NumberOfRecords: 1,
		FileFormat:      "json",
		Name:            "test",
		IncludeHeader:   true,
		Columns: []mockagen.MockagenColumn{
			{
				Name: "id",
				Type: "GUID",
			},
			{
				Name: "username",
				Type: "username",
			},
			{
				Name: "firstName",
				Type: "first_name",
			},
			{
				Name: "lastName",
				Type: "last_name",
			},
			{
				Name: "dob",
				Type: "date",
			},
		},
	}
	for _, size := range inputs {
		b.Run(fmt.Sprintf("generate_fake_%d", size), func(b *testing.B) {
			config.NumberOfRecords = size
			for i := 0; i < b.N; i++ {
				generateFakes(config)
			}
		})
	}
}
