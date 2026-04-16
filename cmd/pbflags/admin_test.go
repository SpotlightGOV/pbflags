package main

import (
	"flag"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// ── parseFlagValue ──────────────────────────────────────────────────

func TestParseFlagValue_Bool(t *testing.T) {
	t.Parallel()
	v, err := parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_BOOL, "true")
	require.NoError(t, err)
	require.True(t, v.GetBoolValue())

	v, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_BOOL, "false")
	require.NoError(t, err)
	require.False(t, v.GetBoolValue())

	_, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_BOOL, "nope")
	require.Error(t, err)
}

func TestParseFlagValue_String(t *testing.T) {
	t.Parallel()
	v, err := parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_STRING, "hello")
	require.NoError(t, err)
	require.Equal(t, "hello", v.GetStringValue())

	// Empty string is allowed for STRING type.
	v, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_STRING, "")
	require.NoError(t, err)
	require.Equal(t, "", v.GetStringValue())
}

func TestParseFlagValue_Int64(t *testing.T) {
	t.Parallel()
	v, err := parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_INT64, "42")
	require.NoError(t, err)
	require.Equal(t, int64(42), v.GetInt64Value())

	v, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_INT64, "-1000")
	require.NoError(t, err)
	require.Equal(t, int64(-1000), v.GetInt64Value())

	_, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_INT64, "not-a-number")
	require.Error(t, err)
}

func TestParseFlagValue_Double(t *testing.T) {
	t.Parallel()
	v, err := parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_DOUBLE, "3.14")
	require.NoError(t, err)
	require.Equal(t, 3.14, v.GetDoubleValue())

	v, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_DOUBLE, "-2.5")
	require.NoError(t, err)
	require.Equal(t, -2.5, v.GetDoubleValue())

	_, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_DOUBLE, "pi")
	require.Error(t, err)
}

func TestParseFlagValue_BoolList(t *testing.T) {
	t.Parallel()
	v, err := parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST, "true,false,true")
	require.NoError(t, err)
	require.Equal(t, []bool{true, false, true}, v.GetBoolListValue().GetValues())

	// Empty input → empty list (not error).
	v, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST, "")
	require.NoError(t, err)
	require.Empty(t, v.GetBoolListValue().GetValues())

	_, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST, "true,nope,false")
	require.Error(t, err)
}

func TestParseFlagValue_StringList(t *testing.T) {
	t.Parallel()
	v, err := parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST, "a, b ,c")
	require.NoError(t, err)
	// Whitespace around items should be trimmed.
	require.Equal(t, []string{"a", "b", "c"}, v.GetStringListValue().GetValues())

	v, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST, "")
	require.NoError(t, err)
	require.Empty(t, v.GetStringListValue().GetValues())
}

func TestParseFlagValue_Int64List(t *testing.T) {
	t.Parallel()
	v, err := parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST, "1,2,-3")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, -3}, v.GetInt64ListValue().GetValues())

	_, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST, "1,two,3")
	require.Error(t, err)
}

func TestParseFlagValue_DoubleList(t *testing.T) {
	t.Parallel()
	v, err := parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST, "1.0,2.5,-3.14")
	require.NoError(t, err)
	require.Equal(t, []float64{1.0, 2.5, -3.14}, v.GetDoubleListValue().GetValues())

	_, err = parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST, "1.0,oops")
	require.Error(t, err)
}

func TestParseFlagValue_Unspecified(t *testing.T) {
	t.Parallel()
	_, err := parseFlagValue(pbflagsv1.FlagType_FLAG_TYPE_UNSPECIFIED, "anything")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported flag type")
}

// ── parsePositionalFlags ────────────────────────────────────────────
//
// These tests pin the flags-anywhere behaviour and the negative-number
// handling that pb-wff.22 added.

func TestParsePositionalFlags_FlagsBefore(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	reason := fs.String("reason", "", "")
	pos := parsePositionalFlags(fs, []string{"--reason=fix", "flag-id", "5"})
	require.Equal(t, "fix", *reason)
	require.Equal(t, []string{"flag-id", "5"}, pos)
}

func TestParsePositionalFlags_FlagsAfter(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	reason := fs.String("reason", "", "")
	pos := parsePositionalFlags(fs, []string{"flag-id", "5", "--reason=fix"})
	require.Equal(t, "fix", *reason)
	require.Equal(t, []string{"flag-id", "5"}, pos)
}

func TestParsePositionalFlags_FlagsInterleaved(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	reason := fs.String("reason", "", "")
	jsonOut := fs.Bool("json", false, "")
	pos := parsePositionalFlags(fs, []string{"flag-id", "--reason=fix", "5", "--json"})
	require.Equal(t, "fix", *reason)
	require.True(t, *jsonOut)
	require.Equal(t, []string{"flag-id", "5"}, pos)
}

func TestParsePositionalFlags_NegativeIntPositional(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	reason := fs.String("reason", "", "")
	// -1000 should land in positional, not be interpreted as a flag.
	pos := parsePositionalFlags(fs, []string{"flag-id", "0", "-1000", "--reason=fix"})
	require.Equal(t, "fix", *reason)
	require.Equal(t, []string{"flag-id", "0", "-1000"}, pos)
}

func TestParsePositionalFlags_NegativeDoublePositional(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	reason := fs.String("reason", "", "")
	pos := parsePositionalFlags(fs, []string{"flag-id", "-3.14", "--reason=fix"})
	require.Equal(t, "fix", *reason)
	require.Equal(t, []string{"flag-id", "-3.14"}, pos)
}

func TestParsePositionalFlags_DoubleDashTerminator(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	reason := fs.String("reason", "", "")
	pos := parsePositionalFlags(fs, []string{"--reason=fix", "--", "--looks-like-flag", "x"})
	require.Equal(t, "fix", *reason)
	require.Equal(t, []string{"--looks-like-flag", "x"}, pos)
}

func TestParsePositionalFlags_BoolFlagDoesNotConsumeNext(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "")
	// `--json flag-id` should NOT consume `flag-id` as the value of --json.
	pos := parsePositionalFlags(fs, []string{"--json", "flag-id"})
	require.True(t, *jsonOut)
	require.Equal(t, []string{"flag-id"}, pos)
}

func TestParsePositionalFlags_StringFlagSpaceSeparated(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	admin := fs.String("admin", "", "")
	pos := parsePositionalFlags(fs, []string{"--admin", "http://localhost:9200", "flag-id"})
	require.Equal(t, "http://localhost:9200", *admin)
	require.Equal(t, []string{"flag-id"}, pos)
}

// Smoke check that splitListValue trims whitespace cleanly. Indirect
// coverage of the helper used by parseFlagValue list types.
func TestSplitListValue(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"a", "b", "c"}, splitListValue("a,b,c"))
	require.Equal(t, []string{"a", "b", "c"}, splitListValue(" a , b , c "))
	require.Empty(t, splitListValue(""))
	require.Empty(t, splitListValue("  "))
	// One element case.
	require.Equal(t, []string{"only"}, splitListValue("only"))
	// Embedded space inside an element is preserved (trim only at the edges).
	require.Equal(t, []string{"a b", "c d"}, splitListValue("a b, c d"))
	// strings unused outside test sanity check below.
	_ = strings.TrimSpace
}
