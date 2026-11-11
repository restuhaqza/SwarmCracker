package netpolicy

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
)

// The interface half of a sandbox's network: the devices inside its namespace and
// the address the guest's default route points at.
//
// Everything here runs as `ip netns exec <name> ...`: the commands are the node's
// own iproute2, entered into the sandbox's namespace, because the devices a
// sandbox owns are devices of that namespace and not of the node. Two sandboxes
// therefore both create, address and raise a device called tap0 without either
// call seeing the other's, which is the isolation this package exists for.

// configureLink gives the namespace its loopback, the fixed tap0 the VMM's
// interface is attached to, and the sandbox's gateway address on it.
//
// The device is created before the address and raised after, so a half-configured
// namespace is never up: a namespace whose tap0 is up without an address would
// answer ARP for nothing and black-hole the guest's first packets, and one that is
// up before the policy is applied is the window Attach's order exists to close.
func (m *Manager) configureLink(ctx context.Context, n Network) error {
	if err := m.inNamespace(ctx, n.Name, "link", "set", "lo", "up"); err != nil {
		return fmt.Errorf("netpolicy: bring up the loopback of %s: %w", n.Name, err)
	}
	if err := m.ensureTap(ctx, n.Name); err != nil {
		return err
	}
	if err := m.inNamespace(ctx, n.Name, "addr", "replace", gatewayCIDR(n.Gateway), "dev", TapName); err != nil {
		return fmt.Errorf("netpolicy: give %s the gateway address %s: %w", TapName, n.Gateway, err)
	}
	if err := m.inNamespace(ctx, n.Name, "link", "set", TapName, "up"); err != nil {
		return fmt.Errorf("netpolicy: bring up %s in %s: %w", TapName, n.Name, err)
	}
	return nil
}

// ensureTap creates the namespace's tap0 unless it is already there.
//
// `ip tuntap add` refuses a device that exists, so the retried attach of a sandbox
// whose namespace survived its manager needs the probe: the device is the same
// device, and recreating it is neither possible nor wanted. Firecracker opens the
// device itself when its interface is configured and is content with the one
// iproute2 makes — it asks for the same tun flags ip(8) sets — which is what lets
// the sandbox's TAP be the namespace's fixed name rather than a name the VMM
// chooses.
func (m *Manager) ensureTap(ctx context.Context, name string) error {
	if err := m.inNamespace(ctx, name, "link", "show", TapName); err == nil {
		return nil
	}
	if err := m.inNamespace(ctx, name, "tuntap", "add", "dev", TapName, "mode", "tap"); err != nil {
		return fmt.Errorf("netpolicy: create %s in %s: %w", TapName, name, err)
	}
	return nil
}

// inNamespace runs one iproute2 command inside a sandbox's namespace.
func (m *Manager) inNamespace(ctx context.Context, name string, args ...string) error {
	full := append([]string{"netns", "exec", name, m.ip}, args...)
	if _, err := m.exec(ctx, m.ip, full...); err != nil {
		return fmt.Errorf("ip %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// gatewayCIDR is the gateway's address with the sandbox prefix, as iproute2 takes
// it ("10.63.0.1/30").
func gatewayCIDR(gateway netip.Addr) string {
	return gateway.String() + fmt.Sprintf("/%d", PrefixLength)
}
