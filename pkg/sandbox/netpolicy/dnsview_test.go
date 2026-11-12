package netpolicy

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

// viewPlan is one sandbox's allowlist as the view sees it: one name, two addresses,
// and one destination addressed literally (which no name answers for).
func viewPlan() []Destination {
	return []Destination{
		{
			Host:      "api.example.com",
			Port:      8443,
			Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("198.51.100.8")},
		},
		{
			Host:      "203.0.113.10",
			Port:      443,
			Addresses: []netip.Addr{netip.MustParseAddr("203.0.113.10")},
		},
	}
}

// query builds one DNS query as a client sends it.
func query(t *testing.T, name string, qtype dnsmessage.Type) []byte {
	t.Helper()

	packed, err := (&dnsmessage.Message{
		Header: dnsmessage.Header{RecursionDesired: true, ID: 0x1234},
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName(name),
			Type:  qtype,
			Class: dnsmessage.ClassINET,
		}},
	}).Pack()
	require.NoError(t, err)
	return packed
}

// replyTo asks a view one query and unpacks the answer it gave.
func replyTo(t *testing.T, view *dnsView, name string, qtype dnsmessage.Type) dnsmessage.Message {
	t.Helper()

	packed, ok := view.answer(query(t, name, qtype))
	require.True(t, ok, "the view answered nothing at all")

	var reply dnsmessage.Message
	require.NoError(t, reply.Unpack(packed))
	require.True(t, reply.Response, "an answer is a response")
	assert.Equal(t, uint16(0x1234), reply.ID, "the id a client matches its question on")
	return reply
}

// addressesIn is the A records one reply carries, as strings.
func addressesIn(t *testing.T, reply dnsmessage.Message) []string {
	t.Helper()

	var out []string
	for _, answer := range reply.Answers {
		resource, ok := answer.Body.(*dnsmessage.AResource)
		require.True(t, ok, "the view answers with A records only")
		assert.Equal(t, dnsmessage.TypeA, answer.Header.Type)
		assert.Equal(t, dnsmessage.ClassINET, answer.Header.Class)
		out = append(out, netip.AddrFrom4(resource.A).String())
	}
	return out
}

// TestTheViewAnswersTheNamesTheAllowlistCarries: the names a sandbox may resolve are
// exactly the addresses it may reach, so the answer is the attach's own resolution.
func TestTheViewAnswersTheNamesTheAllowlistCarries(t *testing.T) {
	view := newDNSView(viewPlan())
	assert.Equal(t, []string{"api.example.com"}, view.known(), "a literal is not a name to resolve")

	reply := replyTo(t, view, "api.example.com.", dnsmessage.TypeA)
	assert.Equal(t, dnsmessage.RCodeSuccess, reply.RCode)
	assert.Equal(t, []string{"198.51.100.7", "198.51.100.8"}, addressesIn(t, reply))
	assert.NotZero(t, reply.Answers[0].Header.TTL, "an answer a client may cache has a lifetime")
}

// TestTheViewSaysEveryOtherNameDoesNotExist: a name outside the allowlist is not
// resolved by the sandbox's resolver — not answered with the wrong address, and not
// passed on to a resolver the guest does not have. The names here are real public
// names, which is what makes the check about the view's policy rather than about
// whether some name exists.
func TestTheViewSaysEveryOtherNameDoesNotExist(t *testing.T) {
	view := newDNSView(viewPlan())

	for _, name := range []string{"example.org.", "api.example.com.evil.test.", "example.com."} {
		reply := replyTo(t, view, name, dnsmessage.TypeA)
		assert.Equal(t, dnsmessage.RCodeNameError, reply.RCode, "%s must not resolve", name)
		assert.Empty(t, reply.Answers, "%s must carry no answer", name)
	}
}

// TestTheViewAnswersNoRecordOfAnotherType: a name the view knows exists, so a query
// for a type it has no record of is NODATA rather than NXDOMAIN. A client that asks
// for AAAA first (as every modern resolver does) must fall back to A, and an
// NXDOMAIN here would make it give up instead.
func TestTheViewAnswersNoRecordOfAnotherType(t *testing.T) {
	view := newDNSView(viewPlan())

	for _, qtype := range []dnsmessage.Type{dnsmessage.TypeAAAA, dnsmessage.TypeMX, dnsmessage.TypeTXT} {
		reply := replyTo(t, view, "api.example.com.", qtype)
		assert.Equal(t, dnsmessage.RCodeSuccess, reply.RCode)
		assert.Empty(t, reply.Answers, "there is no record of that type in this view")
	}
}

// TestTheViewDoesNotCareHowTheNameIsWritten: a resolver's clients do not agree on
// case or on the root label, and one name is one name.
func TestTheViewDoesNotCareHowTheNameIsWritten(t *testing.T) {
	view := newDNSView(viewPlan())

	for _, name := range []string{"api.example.com.", "API.Example.COM.", "Api.Example.Com."} {
		reply := replyTo(t, view, name, dnsmessage.TypeA)
		assert.Equal(t, dnsmessage.RCodeSuccess, reply.RCode, "%s is the name the allowlist carries", name)
		assert.Len(t, reply.Answers, 2)
	}
}

// TestTheViewDropsWhatIsNotAQuestion: the view answers questions. A packet that is
// not one gets no reply at all, so a malformed or spoofed packet cannot make the
// sandbox's resolver believe something about its own view.
func TestTheViewDropsWhatIsNotAQuestion(t *testing.T) {
	view := newDNSView(viewPlan())

	_, ok := view.answer([]byte{0x01, 0x02, 0x03})
	assert.False(t, ok, "a truncated packet is not a question")

	answer, err := (&dnsmessage.Message{
		Header:    dnsmessage.Header{Response: true},
		Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("api.example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}).Pack()
	require.NoError(t, err)
	_, ok = view.answer(answer)
	assert.False(t, ok, "a response arriving as a query is not a question")

	twoQuestions, err := (&dnsmessage.Message{
		Questions: []dnsmessage.Question{
			{Name: dnsmessage.MustNewName("api.example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
			{Name: dnsmessage.MustNewName("example.org."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
		},
	}).Pack()
	require.NoError(t, err)
	_, ok = view.answer(twoQuestions)
	assert.False(t, ok, "a view that answered one of two questions would be guessing")
}

// TestAViewOfOnlyLiteralsKnowsNothing: an allowlist of addresses has no name in it,
// so the view has nothing to answer — which is why a sandbox with such an allowlist
// gets no resolver at all rather than a resolver that says no to everything.
func TestAViewOfOnlyLiteralsKnowsNothing(t *testing.T) {
	view := newDNSView([]Destination{{
		Host:      "203.0.113.10",
		Port:      443,
		Addresses: []netip.Addr{netip.MustParseAddr("203.0.113.10")},
	}})
	assert.Empty(t, view.known())
}

// TestTheViewRefusesAQueryItCannotRead: a class the view does not serve is refused
// rather than answered from the name's records, which is what keeps a question about
// another namespace of names from being answered by this one.
func TestTheViewRefusesWhatItCannotServe(t *testing.T) {
	view := newDNSView(viewPlan())

	packed, err := (&dnsmessage.Message{
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName("api.example.com."),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassCHAOS,
		}},
	}).Pack()
	require.NoError(t, err)

	answer, ok := view.answer(packed)
	require.True(t, ok)
	var reply dnsmessage.Message
	require.NoError(t, reply.Unpack(answer))
	assert.Equal(t, dnsmessage.RCodeRefused, reply.RCode)
	assert.Empty(t, reply.Answers)
}
