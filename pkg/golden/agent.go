package golden

import (
	"fmt"
	"strconv"
	"strings"

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
//
// Caps records what the agent in the image actually advertised on its first boot,
// which is what makes the refusals precise. Protocol and version alone cannot tell
// an agent that predates an operation from one that has it: both answer "2".
type AgentPin struct {
	// Protocol is the guest agent protocol version, as a decimal string.
	Protocol string `yaml:"protocol,omitempty" json:"agentProtocol"`
	// Version is the guest agent release embedded in the image.
	Version string `yaml:"version,omitempty" json:"agentVersion"`
	// Caps lists the operations the guest answered with when the template was
	// proven, in the order the agent advertised them. It is what the host holds a
	// template to (CheckAgentPin): a template whose agent was never asked has no
	// caps here and is refused rather than assumed to have them.
	Caps []string `yaml:"caps,omitempty" json:"agentCaps,omitempty"`
}

// Supports reports whether the pinned agent advertised op.
func (p AgentPin) Supports(op string) bool {
	for _, cap := range p.Caps {
		if cap == op {
			return true
		}
	}
	return false
}

// RequiredAgentCaps is the set of operations a template's guest agent must
// advertise for the host to drive it. It is deliberately smaller than the agent's
// whole capability set: these are the operations whose absence would leave a
// running instance in a state the node cannot fix from outside, which is why a
// template that lacks one is refused instead of used.
//
// `prepare` is the post-restore hook (SBX-083): a clone restored from such a
// template would keep the identity — MAC, address, machine id, host keys — of the
// machine the checkpoint came from, because Firecracker refuses to rewrite any of
// it from the host (RFC D10).
var RequiredAgentCaps = []string{agent.CapPrepare}

// IncompatibleError reports a template whose pinned guest agent this host cannot
// drive. Protocol is empty for a template built before pinning existed, and Caps
// is empty for one built before the agent's capabilities were recorded.
type IncompatibleError struct {
	Template string
	Protocol string
	Minimum  string
	// Caps is what the template recorded of the agent's capabilities.
	Caps []string
	// Missing is the set of operations the host requires and the template does not
	// advertise.
	Missing []string
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
	if len(e.Missing) > 0 {
		advertised := "no recorded capabilities"
		if len(e.Caps) > 0 {
			advertised = "capabilities [" + strings.Join(e.Caps, " ") + "]"
		}
		return fmt.Sprintf("%s: %s carries a guest agent that does not advertise [%s]: it pinned protocol %s with %s; this host needs [%s] — rebuild the template with a current agent",
			CodeTemplateIncompatible, template, strings.Join(e.Missing, " "), pinned, advertised,
			strings.Join(RequiredAgentCaps, " "))
	}
	return fmt.Sprintf("%s: %s pins guest agent protocol %s, this host requires at least %s",
		CodeTemplateIncompatible, template, pinned, e.Minimum)
}

// Code returns the stable failure code, so a caller can branch without matching
// message text.
func (e *IncompatibleError) Code() string { return CodeTemplateIncompatible }

// CheckAgentPin reports whether this host can drive a template whose *recorded*
// agent is pin: the protocol must be at least this build's, and the agent must
// advertise every operation in RequiredAgentCaps. It is the check a manifest goes
// through, and the single place the minimum is enforced.
//
// An unpinned template predates pinning, and one with no recorded capabilities was
// never asked what its agent could do: both are refused for the same reason as an
// explicitly old one, rather than treated as a wildcard.
func CheckAgentPin(name string, pin AgentPin) error {
	if err := CheckAgentProtocol(name, pin.Protocol); err != nil {
		return err
	}
	var missing []string
	for _, cap := range RequiredAgentCaps {
		if !pin.Supports(cap) {
			missing = append(missing, cap)
		}
	}
	if len(missing) > 0 {
		return &IncompatibleError{
			Template: name,
			Protocol: pin.Protocol,
			Minimum:  MinimumAgentProtocol,
			Caps:     pin.Caps,
			Missing:  missing,
		}
	}
	return nil
}

// CheckAgentProtocol reports whether a declared agent protocol is one this host
// can drive. It is the narrower check a *recipe* goes through: a recipe declares
// the minimum protocol it wants its image built with, while the operations that
// image's agent really advertises are what the build records (verifyFirstBoot) and
// what CheckAgentPin holds the finished template to.
func CheckAgentProtocol(name, protocol string) error {
	if protocolAtLeast(protocol, MinimumAgentProtocol) {
		return nil
	}
	return &IncompatibleError{Template: name, Protocol: protocol, Minimum: MinimumAgentProtocol}
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
