package netpolicy

import (
	"net/netip"
	"sort"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

// The DNS view: the sandbox's own answer about which names exist.
//
// The guest's resolver is its gateway (slice one made it so), and the gateway is
// where this view listens. It answers the names the allowlist carries, with the
// addresses the node resolved for them at attach — the same addresses the sandbox
// namespace accepts on the same ports — and it says that every other name does not
// exist. A sandbox's resolver therefore names exactly what the sandbox may reach,
// and the guest has no second resolver to ask: the namespace has no path to one.
//
// NXDOMAIN is the honest answer and not a refusal in disguise: inside this
// sandbox, a name outside the allowlist does not exist. A client that would have
// reached it fails to resolve, which is the first of the two failures the allowlist
// is meant to cause (the second being a connection that cannot be made).

// viewTTL is how long a guest may cache an answer. It is short because the
// addresses are the attach's resolution and a new attach may resolve to different
// ones: a guest that cached them for an hour would keep using an address this
// sandbox no longer accepts.
const viewTTL = 30

// opcodeQuery is the only opcode a view answers: an ordinary query. dnsmessage
// names resource types but not opcodes, and 0 is the opcode every client sends.
const opcodeQuery dnsmessage.OpCode = 0

// dnsView answers the names one sandbox's allowlist carries. It is created once per
// attach and never changes: the addresses its answers name are the addresses the
// namespace was given, so a stale answer and a hole in the policy cannot disagree.
type dnsView struct {
	names map[string][]netip.Addr
}

// newDNSView is the view of a plan: one entry per allowed name, holding every
// address that name was resolved to for any of its ports.
func newDNSView(plan []Destination) *dnsView {
	view := &dnsView{names: make(map[string][]netip.Addr)}
	for _, d := range plan {
		if !d.named() {
			continue
		}
		key := viewName(d.Host)
		for _, addr := range d.Addresses {
			view.names[key] = appendNew(view.names[key], addr)
		}
	}
	return view
}

// known is the names this view answers, sorted: what a caller shows to say what
// the view was built from.
func (v *dnsView) known() []string {
	out := make([]string, 0, len(v.names))
	for name := range v.names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// answer turns one query into the reply this view gives it, and reports whether the
// query deserves a reply at all. A malformed query, a response arriving as a query
// and a query with more than one question are dropped: the guest's resolver is not
// asking, or something is speaking the protocol badly.
func (v *dnsView) answer(query []byte) ([]byte, bool) {
	var msg dnsmessage.Message
	if err := msg.Unpack(query); err != nil {
		return nil, false
	}
	if msg.Response || len(msg.Questions) != 1 {
		return nil, false
	}
	question := msg.Questions[0]

	reply := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:                 msg.ID,
			Response:           true,
			OpCode:             msg.OpCode,
			RecursionDesired:   msg.RecursionDesired,
			RecursionAvailable: true,
		},
		Questions: msg.Questions,
	}

	if msg.OpCode != opcodeQuery {
		reply.RCode = dnsmessage.RCodeNotImplemented
		return packReply(reply)
	}
	if question.Class != dnsmessage.ClassINET {
		reply.RCode = dnsmessage.RCodeRefused
		return packReply(reply)
	}

	addresses, allowed := v.names[viewName(question.Name.String())]
	if !allowed {
		reply.RCode = dnsmessage.RCodeNameError
		return packReply(reply)
	}

	// A name this view knows answers A and "any type" with its addresses; every
	// other type is a name that exists with no record of that type, which is what
	// makes a client asking for AAAA fall back to A instead of failing.
	if question.Type == dnsmessage.TypeA || question.Type == dnsmessage.TypeALL {
		for _, addr := range addresses {
			reply.Answers = append(reply.Answers, dnsmessage.Resource{
				Header: dnsmessage.ResourceHeader{
					Name:  question.Name,
					Type:  dnsmessage.TypeA,
					Class: dnsmessage.ClassINET,
					TTL:   viewTTL,
				},
				Body: &dnsmessage.AResource{A: addr.As4()},
			})
		}
	}
	return packReply(reply)
}

// packReply serialises an answer, and reports a reply that could not be built as no
// reply at all rather than sending something half-written.
func packReply(reply dnsmessage.Message) ([]byte, bool) {
	packed, err := reply.Pack()
	if err != nil {
		return nil, false
	}
	return packed, true
}

// viewName is a name as this view keys it: case-insensitive and without the root
// label, because "Example.COM." and "example.com" are one name to a resolver.
func viewName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// appendNew adds an address the slice does not already hold.
func appendNew(addresses []netip.Addr, addr netip.Addr) []netip.Addr {
	for _, held := range addresses {
		if held == addr {
			return addresses
		}
	}
	return append(addresses, addr)
}
