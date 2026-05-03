# SwarmCracker Documentation

> Firecracker microVM orchestration with SwarmKit — no Docker, no Kubernetes required.

---

## How Docs Are Organized

```
docs/
├── user/           # 👤 If you're running SwarmCracker, start here
├── dev/            # 🔧 If you're contributing or hacking on the code
├── planning/       # 📋 Implementation plans & roadmap
├── research/       # 🔬 Research notes & archived docs
└── site/           # 🌐 GitHub Pages website
```

---

## Find What You Need

### For Users (Running SwarmCracker)

| You want to... | Go here |
|----------------|---------|
| **Install SwarmCracker** | [User Docs → Getting Started](user/getting-started/) |
| **Set up your cluster** | [User Docs → Configuration](user/guides/configuration.md) |
| **Deploy services** | [User Docs → SwarmKit](user/guides/swarmkit.md) |
| **Look up commands** | [User Docs → CLI Reference](user/reference/cli.md) |
| **Understand how it works** | [User Docs → Architecture](user/architecture/) |

### For Developers (Contributing Code)

| You want to... | Go here |
|----------------|---------|
| **Contribute** | [Dev Docs → Contributing](dev/contributing.md) |
| **Run tests** | [Dev Docs → Testing](dev/testing/) |
| **Follow code style** | [Dev Docs → Conventions](dev/conventions.md) |

### Planning & Research

| You want to... | Go here |
|----------------|---------|
| **See what's planned** | [Planning → TODO Implementation](planning/todo-implementation.md) |
| **Read research notes** | [Research → Image Preparation](research/) |

---

## Versions (What's What)

| Component | Version |
|-----------|---------|
| SwarmCracker | v0.6.0 |
| Firecracker | v1.15.1 |
| SwarmKit | v2.1.1 |
| Go | 1.21+ |

---

## Useful Links

- **[GitHub Repo](https://github.com/restuhaqza/SwarmCracker)** — source code, issues, PRs
- **[Firecracker Docs](https://github.com/firecracker-microvm/firecracker)** — the VM engine we use
- **[SwarmKit Docs](https://github.com/moby/swarmkit)** — the orchestration layer
- **[Report a Bug](https://github.com/restuhaqza/SwarmCracker/issues)** — found something? tell us!

---

**Last updated:** 2026-04-19 | **Doc version:** 3.0
