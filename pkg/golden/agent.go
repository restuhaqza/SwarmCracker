package golden

import (
	"fmt"
	"strconv"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

// CodeTemplateIncompatible is the stable code for a template whose embedded
// guest agent is older than the host can drive. It is the contract; the message
// next to it is not.
const CodeTemplateIncompatible = "template_incompatible"

// MinimumAgentProtocol is the oldest guest agent protocol a template may pin. It
// tracks the protocol this build speaks: a template pinned below it carries an
// agent that cannot advertise the operations this host relies on, and no
// fallback exists for a missing operation.
var MinimumAgentProtocol = agent.Version

// AgentPin pins the guest agent a template was built with. A host refuses a
// template whose protocol is older than its minimum, so an operation added in a
// later release can never be skipped silently: the template either carries an
// agent that advertises the operation or the template is not usable at all.
type AgentPin struct {
	// Protocol is the guest agent protocol version, as a decimal string.
	Protocol string `yaml:"protocol,omitempty" json:"agentProtocol"`
	// Version is the guest agent release embedded in the image.
	Version string `yaml:"version,omitempty" json:"agentVersion"`
}

// IncompatibleError reports a template whose pinned guest agent this host cannot
// drive. Protocol is empty for a template built before pinning existed.
type IncompatibleError struct {
	Template string
	Protocol string
	Minimum  string
}

// Error implements error.
func (e *IncompatibleError) Error() string {
	template := e.Template
	if template == "" {
		template = "template"
	}
	pinned := e.Protocol
	if pinned == "" {
		pinned = "unpinned"
	}
	return fmt.Sprintf("%s: %s pins guest agent protocol %s, this host requires at least %s",
		CodeTemplateIncompatible, template, pinned, e.Minimum)
}

// Code returns the stable failure code, so a caller can branch without matching
// message text.
func (e *IncompatibleError) Code() string { return CodeTemplateIncompatible }

// CheckAgentPin reports whether this host can drive a template pinned to pin. It
// is the single place the minimum is enforced: an unpinned template predates
// pinning and is refused for the same reason as an explicitly old one, because
// neither can advertise an operation added after it was built.
func CheckAgentPin(name string, pin AgentPin) error {
	if protocolAtLeast(pin.Protocol, MinimumAgentProtocol) {
		return nil
	}
	return &IncompatibleError{Template: name, Protocol: pin.Protocol, Minimum: MinimumAgentProtocol}
}

// protocolAtLeast reports whether version a is at least b. Both must be decimal,
// so "10" outranks "9"; anything else cannot be shown to be recent enough and is
// treated as too old.
func protocolAtLeast(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr != nil || berr != nil {
		return false
	}
	return ai >= bi
}
