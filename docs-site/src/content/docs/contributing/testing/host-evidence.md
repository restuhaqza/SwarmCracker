---
title: "Host evidence: the MVP's acceptance run"
description: "How the one path that proves what a runner cannot — the sandbox MVP on a real KVM host — is dispatched, what it checks, and what it will not claim."
---

A unit test can say what the code decides. It cannot say that a microVM booted, that a
guest reached nothing but its allowlist, or that a removal left no TAP device behind.
Those answers need KVM, a network namespace and the jailer, so they are produced by one
path: [`.github/workflows/host-evidence.yml`](https://github.com/restuhaqza/SwarmCracker/blob/main/.github/workflows/host-evidence.yml),
which dispatches over SSH into the lab host and runs
[`test/acceptance`](https://github.com/restuhaqza/SwarmCracker/tree/main/test/acceptance).

## The MVP it proves

One node, one operator, the whole cycle:

```
create → exec → cp → expose → checkpoint → fork --count 2 → rm
```

plus the rules that make handing a sandbox to an agent defensible: a sandbox with egress
`none` reaches nothing, a token bound to one sandbox cannot leave it, a non-root guest
process cannot obtain the agent's credential, and `rm` frees every resource it names.

| Epic criterion (#121) | What the run does |
|---|---|
| 1 (cold) | creates a sandbox and checks the node's own ready window against 4 s. The ≤500 ms **warm** half is a pool and is not measured — there is none |
| 2 | `exec` returns stdout, stderr and the guest's exit code, separately |
| 3 | `cp` writes a tree into `/workspace`, `exec` reads it back, and it copies out again |
| 4 | `expose` gives an authenticated URL: 200 with its token, 401 without it, dead once un-exposed or removed |
| 5 | egress `none`: no DNS answer, no path to another sandbox, none to the host's address, none to the internet |
| 6 | `checkpoint` then `fork --count 2` yields three sandboxes with distinct namespaces and addresses, whose writes do not leak either way |
| 8 | `rm` reports every resource freed, and the node has no namespace, disk or VMM of that sandbox afterwards |
| 9 | a sandbox-scoped token execs in its own sandbox and is refused another's, the node's list, and minting, each with `details.reason` |
| 11 | the template's manifest records its agent's protocol and capabilities, a create's answer is the guest's own hello, and a template recording no agent is refused here |
| 12 | the template's non-root user cannot read the agent credential, and a host dial without that credential is rejected while the minted one is accepted |

Outside the MVP, and therefore not run: a warm pool, a second node, pause/resume,
`sandbox shell`, `attach`, the template listing route, `activity`/`quiesce` consistency
under write load, and a cross-node restore.

## Running it

A maintainer dispatches it from the Actions tab (**Host evidence (KVM lab)** → *Run
workflow*), naming the template to boot and whether to keep the run's scratch tree. A
pull request can ask for it by adding the `host-evidence` label.

It is never a required check: the host is single, stateful and shared, so a run has to be
a decision, and a lab outage must not be able to block a merge.

The run needs four repository secrets, and fails — loudly, naming the ones that are
missing — when they are not set:

| Secret | What it is |
|---|---|
| `KVM_HOST` | the lab host's address |
| `KVM_HOST_USER` | the login user; it needs `sudo -n` for KVM, netns and the jailer |
| `KVM_SSH_KEY` | that user's private key, newline-preserved |
| `KVM_HOST_KEY` | the host's public key line, for a pinned `known_hosts` |

The host key is pinned from the secret rather than learned, so a changed host key is a
hard failure and not a prompt nobody reads. Nothing the workflow echoes is a secret: the
key and the host key are written to files and only their paths are logged.

## Cleanliness is part of the test

The lab host carries leftovers from earlier work — bridges, tap devices, an orphaned VMM.
The run does not try to clean somebody else's: it creates its own state directory,
socket, jail base and rootfs under one scratch tree, and it takes a footprint of the host
before and after (sandbox namespaces, preview listeners on derived loopback addresses,
Firecracker processes naming a sandbox). A single name in the "after" that was not in the
"before" fails the run, whatever the tests said. A sandbox nobody removed is a sandbox the
next run would inherit.

## When it fails

- **The host is unreachable**: the job is red and says so. A skip would claim evidence
  that did not happen.
- **A secret is missing**: red, naming it.
- **A criterion fails**: red, and the log names which one and what it observed.
- **The host has no older template**: red. One criterion boots a template whose agent
  predates the `rooted` capability, because that is the case a unit test cannot see — its
  manifest is searched for in the node's template stores, and a host that has none has not
  measured it.
- **Something was left behind**: red, listing what.

The run's log and the criteria it recorded are uploaded as an artifact for 90 days,
because the log is the evidence: it holds the commands, the guest's own output, and the
host's answers. `criteria.tsv` has one line per criterion, ending in `PASS`, `FAIL` or
`NOT EVALUATED` with the reason, and the runner fails on any red or unmeasured one — a
criterion that could not be evaluated is not a criterion that held.

Every criterion stands alone: each creates what it needs and is recorded whatever it
decides, so one red criterion does not hide the ones after it. The first run on the lab
host lost seven criteria to a single failed assertion and reported that as one failure;
that is why the harness is built this way, and why it has its own tests (which run without
KVM, in `test/acceptance/harness_self_test.go`).
