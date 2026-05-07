---
title: SwarmCracker - Firecracker MicroVMs with SwarmKit Orchestration
description: Run containers with hardware-level security, fast boot, and SwarmKit orchestration
---

# Firecracker MicroVMs with SwarmKit Orchestration

Run containers as isolated microVMs with hardware-level security, fast startup, and production-ready orchestration features.

<div class="quick-start" markdown="1">

[Get Started](user/getting-started/README.md){ .md-button .md-button--primary }
[View on GitHub](https://github.com/restuhaqza/SwarmCracker){ .md-button }

</div>

---

## Why SwarmCracker?

<div class="stats-grid" markdown="1">

<div class="stat" markdown="1">
<span class="stat-icon">⚡</span>
<div class="stat-value">&lt; 100ms</div>
<div class="stat-label">MicroVM Boot Time</div>
</div>

<div class="stat" markdown="1">
<span class="stat-icon">💾</span>
<div class="stat-value">&lt; 5MB</div>
<div class="stat-label">Memory Overhead</div>
</div>

<div class="stat" markdown="1">
<span class="stat-icon">🛡️</span>
<div class="stat-value">100%</div>
<div class="stat-label">KVM Isolation</div>
</div>

<div class="stat" markdown="1">
<span class="stat-icon">🖥️</span>
<div class="stat-value">Linux</div>
<div class="stat-label">Native Support</div>
</div>

</div>

---

## Features

<div class="features-grid" markdown="1">

<div class="feature-card" markdown="1">
<span class="feature-icon">🔥</span>
<h3>MicroVM Isolation</h3>
<p>Each container gets its own kernel via KVM, providing hardware-level security and strong workload isolation.</p>
</div>

<div class="feature-card" markdown="1">
<span class="feature-icon">🔄</span>
<h3>SwarmKit Orchestration</h3>
<p>Services, scaling, rolling updates, secrets management - all the features you expect from modern orchestration.</p>
</div>

<div class="feature-card" markdown="1">
<span class="feature-icon">⚡</span>
<h3>Fast Startup</h3>
<p>MicroVMs boot in milliseconds with minimal overhead, combining container speed with VM security.</p>
</div>

<div class="feature-card" markdown="1">
<span class="feature-icon">🛡️</span>
<h3>Hardware Security</h3>
<p>KVM virtualization provides stronger isolation than container namespaces, protecting against kernel exploits.</p>
</div>

<div class="feature-card" markdown="1">
<span class="feature-icon">🌐</span>
<h3>VXLAN Networking</h3>
<p>Cross-node VM communication with VXLAN overlay networks, supporting multi-node clusters out of the box.</p>
</div>

<div class="feature-card" markdown="1">
<span class="feature-icon">📊</span>
<h3>Rolling Updates</h3>
<p>Zero-downtime deployments with health monitoring and automatic rollback on failure.</p>
</div>

</div>

---

## Quick Start

### Initialize Manager

```bash
sudo swarmcracker init

# Or specify IP
sudo swarmcracker init --advertise-addr 192.168.1.10:4242
```

### Get Join Token

```bash
sudo cat /var/lib/swarmkit/join-tokens.txt
```

### Join Workers

```bash
sudo swarmcracker join 192.168.1.10:4242 --token SWMTKN-1-...
```

---

## One-Line Install

```bash
curl -fsSL https://swarmcracker.restuhaqza.dev/install.sh | bash
```

Options: Manager (init cluster), Worker (join existing), Skip (binaries only).

Non-interactive:

```bash
curl -fsSL https://swarmcracker.restuhaqza.dev/install.sh | bash -s -- \
  --worker --manager 192.168.1.10:4242 --token SWMTKN-1-xxxxx
```

---

## Documentation

| Section | Description |
|---------|-------------|
| [Getting Started](user/getting-started/README.md) | Setup and initialization |
| [Networking](user/guides/networking.md) | Bridge, VXLAN, overlay networks |
| [SwarmKit](user/guides/swarmkit.md) | Orchestration features |
| [Configuration](user/guides/configuration.md) | Customize your setup |
| [Security](user/guides/security.md) | Security policies and best practices |
| [CLI Reference](user/reference/cli.md) | Command documentation |

---

## Architecture

```
SwarmKit Manager → swarmd-firecracker → Firecracker VMM → MicroVM
```

Workers run `swarmd-firecracker`, translating SwarmKit tasks into Firecracker configs.

---

## Resources

- [Firecracker](https://firecracker-microvm.github.io/) - MicroVM technology
- [SwarmKit](https://github.com/moby/swarmkit) - Orchestration engine
- [KVM](https://www.linux-kvm.org/) - Hardware virtualization