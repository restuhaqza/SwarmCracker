// Package network provides netlink interface for network operations.
// This file defines the interface for netlink operations to enable mocking in tests.

package network

import (
	"github.com/vishvananda/netlink"
)

// NetlinkExecutor defines the interface for netlink operations.
// This interface allows mocking network operations in tests without requiring CAP_NET_ADMIN.
type NetlinkExecutor interface {
	// Link operations
	LinkByName(name string) (netlink.Link, error)
	LinkAdd(link netlink.Link) error
	LinkDel(link netlink.Link) error
	LinkSetUp(link netlink.Link) error
	LinkSetDown(link netlink.Link) error
	LinkSetMaster(link netlink.Link, master netlink.Link) error
	LinkSetMTU(link netlink.Link, mtu int) error
	LinkSetNsFd(link netlink.Link, fd int) error

	// Address operations
	AddrAdd(link netlink.Link, addr *netlink.Addr) error
	AddrDel(link netlink.Link, addr *netlink.Addr) error
	AddrList(link netlink.Link, family int) ([]netlink.Addr, error)

	// Route operations
	RouteAdd(route *netlink.Route) error
	RouteDel(route *netlink.Route) error
	RouteList(link netlink.Link, family int) ([]netlink.Route, error)

	// Bridge operations
	BridgeVlanAdd(link netlink.Link, vid int) error
	BridgeVlanDel(link netlink.Link, vid int) error

	// Neighbor/FDB operations
	NeighAdd(neigh *netlink.Neigh) error
	NeighDel(neigh *netlink.Neigh) error
	NeighList(linkIndex, family int) ([]netlink.Neigh, error)
}

// DefaultNetlinkExecutor is the default implementation using real netlink calls.
type DefaultNetlinkExecutor struct{}

// NewDefaultNetlinkExecutor creates a new default netlink executor.
func NewDefaultNetlinkExecutor() NetlinkExecutor {
	return &DefaultNetlinkExecutor{}
}

func (e *DefaultNetlinkExecutor) LinkByName(name string) (netlink.Link, error) {
	return netlink.LinkByName(name)
}

func (e *DefaultNetlinkExecutor) LinkAdd(link netlink.Link) error {
	return netlink.LinkAdd(link)
}

func (e *DefaultNetlinkExecutor) LinkDel(link netlink.Link) error {
	return netlink.LinkDel(link)
}

func (e *DefaultNetlinkExecutor) LinkSetUp(link netlink.Link) error {
	return netlink.LinkSetUp(link)
}

func (e *DefaultNetlinkExecutor) LinkSetDown(link netlink.Link) error {
	return netlink.LinkSetDown(link)
}

func (e *DefaultNetlinkExecutor) LinkSetMaster(link netlink.Link, master netlink.Link) error {
	return netlink.LinkSetMaster(link, master)
}

func (e *DefaultNetlinkExecutor) LinkSetMTU(link netlink.Link, mtu int) error {
	return netlink.LinkSetMTU(link, mtu)
}

func (e *DefaultNetlinkExecutor) LinkSetNsFd(link netlink.Link, fd int) error {
	return netlink.LinkSetNsFd(link, fd)
}

func (e *DefaultNetlinkExecutor) AddrAdd(link netlink.Link, addr *netlink.Addr) error {
	return netlink.AddrAdd(link, addr)
}

func (e *DefaultNetlinkExecutor) AddrDel(link netlink.Link, addr *netlink.Addr) error {
	return netlink.AddrDel(link, addr)
}

func (e *DefaultNetlinkExecutor) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	return netlink.AddrList(link, family)
}

func (e *DefaultNetlinkExecutor) RouteAdd(route *netlink.Route) error {
	return netlink.RouteAdd(route)
}

func (e *DefaultNetlinkExecutor) RouteDel(route *netlink.Route) error {
	return netlink.RouteDel(route)
}

func (e *DefaultNetlinkExecutor) RouteList(link netlink.Link, family int) ([]netlink.Route, error) {
	return netlink.RouteList(link, family)
}

func (e *DefaultNetlinkExecutor) BridgeVlanAdd(link netlink.Link, vid int) error {
	return netlink.BridgeVlanAdd(link, uint16(vid), false, false, false, false)
}

func (e *DefaultNetlinkExecutor) BridgeVlanDel(link netlink.Link, vid int) error {
	return netlink.BridgeVlanDel(link, uint16(vid), false, false, false, false)
}

func (e *DefaultNetlinkExecutor) NeighAdd(neigh *netlink.Neigh) error {
	return netlink.NeighAdd(neigh)
}

func (e *DefaultNetlinkExecutor) NeighDel(neigh *netlink.Neigh) error {
	return netlink.NeighDel(neigh)
}

func (e *DefaultNetlinkExecutor) NeighList(linkIndex, family int) ([]netlink.Neigh, error) {
	return netlink.NeighList(linkIndex, family)
}