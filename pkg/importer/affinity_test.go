package importer

import (
	"testing"

	"github.com/catdevman/mockagen/pkg/mockagen/types"
)

func TestAffinityFollowsClassify(t *testing.T) {
	// The picker and the importer have to agree. A second spelling list
	// here would offer Number for a column classify still treats as text.
	two := int64(2)
	zero := int64(0)
	cases := []struct {
		sql   string
		scale *int64
		want  types.SQLAffinity
	}{
		{sql: "integer", want: types.AffinityInt},
		{sql: "numeric(10,0)", want: types.AffinityInt},
		{sql: "numeric(10,2)", want: types.AffinityFloat},
		{sql: "numeric", scale: &zero, want: types.AffinityInt},
		{sql: "numeric", scale: &two, want: types.AffinityFloat},
		{sql: "boolean", want: types.AffinityBool},
		{sql: "timestamp", want: types.AffinityTime},
		{sql: "bytea", want: types.AffinityBinary},
		{sql: "varchar(255)", want: types.AffinityText},
		{sql: "weird_type", want: types.AffinityText},
	}
	for _, tc := range cases {
		got := Affinity(tc.sql, tc.scale)
		if got != tc.want {
			t.Errorf("Affinity(%q) = %q, want %q", tc.sql, got, tc.want)
		}
	}
}
