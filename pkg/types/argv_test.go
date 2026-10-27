package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeArgvBootArgs_RoundTrip(t *testing.T) {
	cases := [][]string{
		{"/bin/false"},
		{"/bin/sh", "-c", "echo hello world"},
		{"nginx", "-g", "daemon off;"},
		{"", "empty-first"},
		{"trailing empty", ""},
		{"quote's", `double "quoted"`, "$HOME", "`date`"},
		{"unicode-✓", "tab\tinside"},
	}

	for _, argv := range cases {
		tokens := EncodeArgvBootArgs(argv)
		require.Len(t, tokens, len(argv))

		got := make([]string, 0, len(tokens))
		for _, tok := range tokens {
			require.True(t, strings.HasPrefix(tok, ArgvBootArgPrefix), "token %q must carry the prefix", tok)
			assert.NotContains(t, tok, " ", "a token must not contain whitespace: the kernel splits on it")
			arg, err := DecodeArgvBootArg(tok)
			require.NoError(t, err)
			got = append(got, arg)
		}
		assert.Equal(t, argv, got)
	}
}

func TestEncodeArgvBootArgs_Empty(t *testing.T) {
	assert.Nil(t, EncodeArgvBootArgs(nil), "no command must add no boot arguments")
	assert.Nil(t, EncodeArgvBootArgs([]string{}))
}

func TestDecodeArgvBootArg_RejectsInvalid(t *testing.T) {
	for _, tok := range []string{
		"",
		"sc.hostname=web",
		ArgvBootArgPrefix + "!!!not-base64",
		ArgvBootArgPrefix, // empty payload decodes to nothing
	} {
		_, err := DecodeArgvBootArg(tok)
		assert.Error(t, err, "token %q must be rejected", tok)
	}
}

func TestValidateArgv(t *testing.T) {
	assert.NoError(t, ValidateArgv("--args", []string{"-c", "echo hello", ""}))
	for _, bad := range []string{"line\nbreak", "carriage\rreturn", "nul\x00byte"} {
		err := ValidateArgv("--command", []string{bad})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--command")
	}
}
