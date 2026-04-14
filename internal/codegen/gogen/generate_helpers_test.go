package gogen

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFlagMetaTypeName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fl   flagInfo
		want string
	}{
		{flagInfo{goType: "bool"}, "flagmeta.FlagTypeBool"},
		{flagInfo{goType: "string"}, "flagmeta.FlagTypeString"},
		{flagInfo{goType: "int64"}, "flagmeta.FlagTypeInt64"},
		{flagInfo{goType: "float64"}, "flagmeta.FlagTypeDouble"},
		{flagInfo{goType: "[]bool", isList: true}, "flagmeta.FlagTypeBool"},
		{flagInfo{goType: "[]string", isList: true}, "flagmeta.FlagTypeString"},
		{flagInfo{goType: "[]int64", isList: true}, "flagmeta.FlagTypeInt64"},
		{flagInfo{goType: "[]float64", isList: true}, "flagmeta.FlagTypeDouble"},
	}
	for _, tt := range tests {
		t.Run(tt.fl.goType, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, flagMetaTypeName(tt.fl))
		})
	}
}

func TestFlagMetaDefaultField(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fl   flagInfo
		want string
	}{
		{flagInfo{goType: "bool"}, "DefaultBool"},
		{flagInfo{goType: "string"}, "DefaultString"},
		{flagInfo{goType: "int64"}, "DefaultInt64"},
		{flagInfo{goType: "float64"}, "DefaultDouble"},
		{flagInfo{goType: "[]bool"}, "DefaultBools"},
		{flagInfo{goType: "[]string"}, "DefaultStrings"},
		{flagInfo{goType: "[]int64"}, "DefaultInt64s"},
		{flagInfo{goType: "[]float64"}, "DefaultDoubles"},
	}
	for _, tt := range tests {
		t.Run(tt.fl.goType, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, flagMetaDefaultField(tt.fl))
		})
	}
}
