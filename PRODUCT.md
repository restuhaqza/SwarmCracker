# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Homelabbers and self-hosters running their own Linux boxes — often single-node, sometimes a small multi-node cluster. They want stronger workload isolation than plain Docker/Swarm containers but do not want to adopt Kubernetes. They typically arrive from GitHub, a community post, or search, and are deciding whether SwarmCracker is worth trying.

## Product Purpose

SwarmCracker runs Docker containers as hardware-isolated Firecracker microVMs behind the familiar Docker Swarm interface. It exists to provide KVM-grade isolation without Kubernetes complexity. For this landing page, success means a first-time visitor understands the mechanism within seconds and feels confident enough to run the one-line installer.

## Positioning

Docker Swarm UX with hardware-isolated microVMs. Each workload boots its own Linux kernel via KVM/Firecracker while orchestration stays SwarmKit. A neighboring container runtime cannot truthfully claim both the familiar Swarm interface and per-workload kernel isolation at once.

## Operating Context

Linux host with KVM (`/dev/kvm`). The product is evaluated by reading a landing page and docs, then running a one-line install script; it is compared against Docker Swarm and Kubernetes.

## Capabilities and Constraints

Confirmed: per-VM kernel, SwarmKit-compatible executor, KVM hardware isolation, ~100 ms boot, built-in VXLAN cross-node networking, rolling updates, jailer support, Apache 2.0 license. Requires Linux + KVM. Terminology: microVM, Firecracker, SwarmKit, TAP device, VXLAN, jailer.

## Brand Commitments

Name: SwarmCracker. Existing copy, factual claims, and links (GitHub, releases, docs.swarmcracker.com, LICENSE) are binding and must be preserved. The visual world is explicitly open to replacement; the incumbent dark near-black + orange (`#FF6B35`) look is an anti-reference, not a commitment.

## Evidence on Hand

Real: install script URL, CLI commands, terminal output sequence, docs URLs, GitHub repo, release tag v0.10.0, Apache 2.0 LICENSE. No customer logos, testimonials, benchmarks, or pricing exist — future work must not fabricate them.

## Product Principles

1. Isolation you can trust — hardware boundaries, stated plainly.
2. Familiar interface — Docker Swarm commands, no new mental model.
3. No Kubernetes tax — strong isolation without control-plane complexity.
4. Show the mechanism, not just the claim.
5. Keep it honest — no invented proof.

## Accessibility & Inclusion

WCAG AA contrast, keyboard focus rings, reduced-motion support, and a single semantic `h1`.
