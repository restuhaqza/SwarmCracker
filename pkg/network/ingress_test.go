//go:build !integration

package network

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ingressTestManager() *NetworkManager {
	return newPortTestManager()
}

func ingressCalls(state *mockState) []string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]string(nil), state.calls...)
}

func TestProgramIngress_SingleBackend(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := ingressTestManager()
	err := nm.ProgramIngress([]IngressRoute{{
		ServiceID: "svc1",
		Ports:     []IngressPort{{Protocol: "tcp", PublishedPort: 8080, TargetPort: 80}},
		Backends:  []string{"192.168.127.5"},
	}})
	require.NoError(t, err)

	calls := ingressCalls(state)
	assert.Contains(t, calls, "iptables -t nat -A PREROUTING -p tcp --dport 8080 -m comment --comment swarmcracker:ingress:svc1 -j DNAT --to-destination 192.168.127.5:80")
	assert.Contains(t, calls, "iptables -t nat -A OUTPUT -p tcp --dport 8080 -m comment --comment swarmcracker:ingress:svc1 -j DNAT --to-destination 192.168.127.5:80")
	assert.Contains(t, calls, "iptables -t nat -A POSTROUTING -d 192.168.127.5/32 -m comment --comment swarmcracker:ingress:svc1 -j MASQUERADE")
}

func TestProgramIngress_RoundRobinsAcrossBackends(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := ingressTestManager()
	err := nm.ProgramIngress([]IngressRoute{{
		ServiceID: "svc1",
		Ports:     []IngressPort{{Protocol: "tcp", PublishedPort: 8080, TargetPort: 80}},
		Backends:  []string{"192.168.127.6", "192.168.127.5"}, // unsorted on input
	}})
	require.NoError(t, err)

	calls := ingressCalls(state)
	// Backends are sorted. Only new connections are distributed, via a
	// probability ladder that guarantees every connection matches a rule.
	assert.Contains(t, calls, "iptables -t nat -A PREROUTING -p tcp --dport 8080 -m conntrack --ctstate NEW -m statistic --mode random --probability 0.500000 -m comment --comment swarmcracker:ingress:svc1 -j DNAT --to-destination 192.168.127.5:80")
	assert.Contains(t, calls, "iptables -t nat -A PREROUTING -p tcp --dport 8080 -m conntrack --ctstate NEW -m comment --comment swarmcracker:ingress:svc1 -j DNAT --to-destination 192.168.127.6:80")
	assert.Contains(t, calls, "iptables -t nat -A POSTROUTING -d 192.168.127.5/32 -m comment --comment swarmcracker:ingress:svc1 -j MASQUERADE")
	assert.Contains(t, calls, "iptables -t nat -A POSTROUTING -d 192.168.127.6/32 -m comment --comment swarmcracker:ingress:svc1 -j MASQUERADE")
}

func TestProgramIngress_Idempotent(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := ingressTestManager()
	routes := []IngressRoute{{
		ServiceID: "svc1",
		Ports:     []IngressPort{{Protocol: "tcp", PublishedPort: 8080, TargetPort: 80}},
		Backends:  []string{"192.168.127.5"},
	}}
	require.NoError(t, nm.ProgramIngress(routes))

	before := len(ingressCalls(state))
	require.NoError(t, nm.ProgramIngress(routes))
	assert.Equal(t, before, len(ingressCalls(state)), "an unchanged reconcile must not touch iptables")
}

func TestProgramIngress_SweepsStaleRules(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C", true)
	state.setOutput("iptables -t nat -S PREROUTING",
		"-A PREROUTING -p tcp -m tcp --dport 9999 -m comment --comment \"swarmcracker:ingress:old\" -j DNAT --to-destination 192.168.127.9:80\n")
	restore := setupMocksForTest(state)
	defer restore()

	nm := ingressTestManager()
	require.NoError(t, nm.ProgramIngress(nil))

	calls := ingressCalls(state)
	assert.Contains(t, calls, "iptables -t nat -D PREROUTING -p tcp -m tcp --dport 9999 -m comment --comment swarmcracker:ingress:old -j DNAT --to-destination 192.168.127.9:80")
}

func TestClearIngress_RemovesRules(t *testing.T) {
	state := newMockState()
	state.setOutput("iptables -t nat -S OUTPUT",
		"-A OUTPUT -p tcp -m tcp --dport 8080 -m comment --comment \"swarmcracker:ingress:svc1\" -j DNAT --to-destination 192.168.127.5:80\n")
	restore := setupMocksForTest(state)
	defer restore()

	nm := ingressTestManager()
	require.NoError(t, nm.ClearIngress())

	calls := ingressCalls(state)
	assert.Contains(t, calls, "iptables -t nat -D OUTPUT -p tcp -m tcp --dport 8080 -m comment --comment swarmcracker:ingress:svc1 -j DNAT --to-destination 192.168.127.5:80")
	assert.Equal(t, "", nm.ingressRules)
}
