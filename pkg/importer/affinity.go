package importer

import "github.com/catdevman/mockagen/pkg/mockagen/types"

// Affinity maps a SQL type spelling onto a generator affinity. It calls
// classify so the picker cannot drift from the importer's kind rules.
func Affinity(sqlType string, scale *int64) types.SQLAffinity {
	switch classify(sqlType, scale) {
	case KindInt:
		return types.AffinityInt
	case KindFloat:
		return types.AffinityFloat
	case KindBool:
		return types.AffinityBool
	case KindTime:
		return types.AffinityTime
	case KindBinary:
		return types.AffinityBinary
	default:
		return types.AffinityText
	}
}
