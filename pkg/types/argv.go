package types

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// ArgvBootArgPrefix is the kernel boot-argument prefix that carries one element
// of a service task's argv to the guest init wrapper.
//
// A container's command and args are per task, while the image rootfs (and the
// init wrapper baked into it) is shared by every task on the node. They
// therefore cannot be baked into /sbin/init; they travel on the kernel command
// line, the same way --hostname and --dns do. One argv element is one boot
// argument, base64-encoded so that spaces, quotes, '$' and non-ASCII bytes
// survive a channel that splits its values on whitespace.
const ArgvBootArgPrefix = "sc.argv="

// argvSentinel prefixes every value before encoding. Base64 of an empty string
// is empty, and an empty token cannot survive the shell word splitting the
// init wrapper uses to walk the sc.argv= arguments, so an empty argument would
// silently disappear. The sentinel guarantees every token is non-empty, and
// the wrapper strips it after decoding.
const argvSentinel = 'x'

// EncodeArgvBootArgs returns one kernel boot argument per argv element, in
// order. It returns nil for an empty argv so a task that specifies no command
// keeps byte-identical boot arguments (the wrapper then runs the image's own
// OCI command).
func EncodeArgvBootArgs(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	out := make([]string, 0, len(argv))
	for _, arg := range argv {
		payload := append([]byte{argvSentinel}, []byte(arg)...)
		out = append(out, ArgvBootArgPrefix+base64.StdEncoding.EncodeToString(payload))
	}
	return out
}

// DecodeArgvBootArg reverses EncodeArgvBootArgs for one boot argument.
func DecodeArgvBootArg(arg string) (string, error) {
	if !strings.HasPrefix(arg, ArgvBootArgPrefix) {
		return "", fmt.Errorf("boot argument %q is not an argv element", arg)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(arg, ArgvBootArgPrefix))
	if err != nil {
		return "", fmt.Errorf("invalid %s payload: %w", ArgvBootArgPrefix, err)
	}
	if len(raw) == 0 || raw[0] != argvSentinel {
		return "", fmt.Errorf("invalid %s payload: missing sentinel", ArgvBootArgPrefix)
	}
	return string(raw[1:]), nil
}

// ValidateArgv rejects argv elements that cannot survive the kernel command
// line. A NUL terminates the kernel's copy of the string, and the init wrapper
// reads each element through a shell command substitution, which silently
// strips trailing line breaks. Both would change the argument behind the
// user's back, so the CLI fails at create time instead.
func ValidateArgv(flag string, argv []string) error {
	for _, arg := range argv {
		if strings.ContainsAny(arg, "\n\r\x00") {
			return fmt.Errorf("%s value %q contains a line break or NUL, which cannot be passed to a service task", flag, arg)
		}
	}
	return nil
}
