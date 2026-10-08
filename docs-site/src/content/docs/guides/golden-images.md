---
title: "Golden Image Recipes"
---

Related: [Snapshots](/guides/snapshots/), `pkg/image/`, `pkg/translator/`, `pkg/swarmkit/translator.go`, `recipes/`

## Status

The recipe loader/validator (`pkg/golden/recipe.go`), the builder
(`pkg/golden/builder.go`, `builder_real.go`), the exported `pkg/image` building
blocks (`PullImage`, `ExtractImageToDir`, `CreateExt4FromDir`, `ParseDiskSize`),
and the `swarmcracker image build|list|inspect` CLI are implemented. Verified on
a real KVM node (amd64, Firecracker v1.15.1). Each recipe was
built fresh, the runtime checked inside the ext4 with `debugfs`, then booted
under Firecracker with a tap NIC and its serial log inspected:

| Recipe | Build | Runtime in image | Guest boot |
|---|---|---|---|
| `almalinux-9-docker` | ✅ | ✅ | ✅ containerd + docker, multi-user |
| `ubuntu-24.04-docker` | ✅ | ✅ | ✅ containerd + docker, multi-user |
| `debian-12-docker` | ✅ | ✅ | ✅ containerd + docker, multi-user |
| `alpine-3.20-docker` | ✅ | ✅ | ✅ `Starting Docker Daemon ... [ ok ]` |

`go test ./pkg/golden/` and `go test ./cmd/swarmcracker/ -run Image` pass and
`go vet` is clean. The reusable harness is
`test-automation/scripts/golden-matrix-test.sh`.

Issues the matrix exposed and the fixes applied to the builder/recipes:

1. **No `/tmp`** in minimal OCI rootfs → `apt`/`apt-key` failed. The chroot
   runner now creates `/tmp`, `/var/tmp`, `/run`, `/var/log`, `/root` first.
2. **apt/dpkg tried to start services** through a non-running init → added a
   `policy-rc.d` (`exit 101`) during provisioning, removed afterwards.
3. **RHEL clones ship `curl-minimal`**, conflicting with `curl` → RPM recipes
   drop `curl` and use `dnf --allowerasing`.
4. **Missing `/etc/sysctl.d`** on AlmaLinux → recipes `mkdir -p` it.
5. **OpenRC `networking` needs `/etc/network/interfaces`** and `docker` depends
   on it → Alpine configures `eth0` from the kernel `ip=` parameter via an init
   script.
6. **90 s `dev-ttyS0.device` stall** without `udev` → systemd recipes now
   install `udev`/`systemd-udev`.

systemd recipes still rely on the kernel `ip=` parameter for addressing; wiring
networkd/NetworkManager for systemd guests is a follow-up.

Booting through the **service** path is also wired: a service can set the
`swarmcracker.golden` label (or use `swarmcracker service create --golden
<name[@version]>`). `Controller.Prepare` resolves the artifact from the golden
store (`--golden-dir`, default `/var/lib/firecracker/golden`), skips OCI image
preparation, and records the rootfs and kernel profile as task annotations so
the SwarmKit translator boots the golden guest with the kernel the recipe
pinned. Each task first **materializes its own writable copy** of the template
under the rootfs dir (`Artifact.Materialize`, reflink/sparse-aware), because
replicas must not share a read/write rootfs and a hardened daemon may not be
able to write to the golden store at all. Removal deletes that copy and never
the golden artifact. A missing artifact fails the task; there is no silent OCI
fallback.

Verified on the KVM node: a `goldsvc` service (label
`swarmcracker.golden=ubuntu-24.04-docker@1.0.0`) booted to `multi-user.target`
from `/usr/share/firecracker/vmlinux-runtime` with the recipe boot args, and
`service rm` removed the per-task copy while the template's md5 stayed
identical.

Booting through the CLI is now wired: `swarmcracker vm create --golden
<name[@version]>` resolves the artifact, marks the task with a prebuilt rootfs,
and the executor/translator boot the recipe's own init with its boot args.
Verified on the node: the Ubuntu 24.04 golden VM reached multi-user with
`dockerd 29.1.3` listening on `/run/docker.sock`, and `vm list` shows it as
`golden:ubuntu-24.04-docker@1.0.0`.

## 1. Problem statement

Today SwarmCracker turns **one OCI image into one microVM that runs exactly one
process tree**. The flow is:

```
OCI image ──pull/extract──> dir ──inject tini + /init wrapper──> mkfs.ext4 ──> boot
                                                                    │
                                        kernel: ... nomodules init=/sbin/init
                                        one writable ext4 root device
```

Two requests are not served by this model:

1. **"Golden images"** — a curated, versioned, pre-built rootfs per distro that
   boots fast and is reused across tasks instead of being re-derived from layers
   every time.
2. **"A microVM that can run a container runtime inside (Docker/containerd)"** —
   a full guest OS where the VM is the host and Docker runs inside it.

These overlap: a golden image is the natural delivery vehicle for a
Docker-capable VM. This document analyses feasibility and defines a **recipe**
format for building both.

## 2. Current-state analysis (grounded in code)

### 2.1 What the preparer does

`pkg/image/preparer.go`:

- Pulls the OCI image with `go-containerregistry` (daemon-free), falls back to
  `docker`/`podman` CLI.
- Extracts the flattened filesystem into a temp dir.
- `DetectInitType` (`pkg/image/detector.go`) classifies the image as
  `scratch | systemd | openrc | sysvinit | tini | dumb-init | none`.
  **`systemd` is treated as `incompatible` and fails preparation.**
- Injects an init **wrapper** (`pkg/image/init.go`, `wrapper.go`): writes
  `/sbin/tini`, generates a `/sbin/init` shell script that mounts proc/sys/dev,
  configures eth0 from the kernel `ip=` arg, exports OCI `ENV`, drops to OCI
  `USER`, and `exec`s the OCI `ENTRYPOINT/CMD` under tini. `/init -> /sbin/init`.
- Injects essentials (`essentials.go`): `resolv.conf`, `hosts`, `nsswitch`,
  `machine-id`, and creates `/tmp`, `/run`, `/var/log`, `/root`.
- Creates an **ext4** with `mkfs.ext4 -d` at content-size + 50 % overhead,
  minimum 100 MiB, honouring a per-service `swarmcracker.disk` label.

### 2.2 What the translator/boot path does

- `pkg/translator/translator.go` and `pkg/swarmkit/translator.go` hardcode
  boot args: `console=ttyS0 reboot=k panic=1 pci=off nomodules init=/sbin/init`
  (`init=/init` in one path), plus `ip=<ip>::<gw>:<mask>::eth0:off`.
- Exactly **one** drive is created: `task.ID` -> the rootfs `.ext4`,
  `is_root_device: true`, `is_read_only: false`.
- `machine-config` is sized from SwarmKit `Resources.Reservations` (min 1 vCPU /
  512 MiB).

### 2.3 What the snapshot package adds

`pkg/snapshot` can pause a VM and write `vm.state` + `vm.mem`, restoring in
2–3× less than cold boot. A **booted golden VM snapshot** is the fastest
possible golden-image delivery — but it pins the exact kernel/rootfs/boot-args,
so it must be versioned with the recipe.

### 2.4 The hard constraints for "Docker inside"

| Constraint | Where it lives today | Impact |
|---|---|---|
| `init=/sbin/init` tini wrapper replaces the real init | `image/init.go`, `wrapper.go` | Must be bypassed for a "VM host" golden image; systemd/OpenRC must own PID 1 |
| systemd rejected as incompatible | `image/detector.go` | Must become allow-listed per recipe |
| `nomodules` boot arg | translators | Fine **if** every needed driver is built-in (`=y`), which we control via the kernel profile |
| one root drive | translators | Docker needs an image store; either enlarge root or add a second writable data disk |
| 100 MiB default rootfs | `preparer.go` | Full distro + Docker needs GiBs; recipe must set disk size |
| `pci=off` | translators | OK — Firecracker uses virtio-mmio, not PCI |
| one process = one task | executor/SwarmKit model | A Docker-capable VM is a *host*; mapping a SwarmKit task to work inside it needs a guest agent (see §6) |

## 3. Kernel feasibility — evidence

I downloaded the Firecracker CI guest kernel config
(`resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config`, 3563 lines) and
grepped the options a container runtime needs:

| Capability | Option | Value |
|---|---|---|
| Overlay storage driver | `CONFIG_OVERLAY_FS` | `=y` |
| Bridge + veth | `CONFIG_BRIDGE`, `CONFIG_VETH` | `=y` |
| Netfilter core | `CONFIG_NETFILTER`, `CONFIG_NF_CONNTRACK`, `CONFIG_NF_TABLES` | `=y` |
| NAT / masquerade | `CONFIG_NF_NAT`, `CONFIG_NETFILTER_XT_TARGET_MASQUERADE`, `CONFIG_IP_NF_NAT`, `CONFIG_IP_NF_TARGET_MASQUERADE` | `=y` |
| iptables filter/mangle | `CONFIG_IP_NF_IPTABLES`, `CONFIG_IP_NF_FILTER`, `CONFIG_IP_NF_MANGLE`, `CONFIG_NETFILTER_XTABLES` | `=y` |
| addrtype / conntrack match | `CONFIG_NETFILTER_XT_MATCH_ADDRTYPE`, `..._CONNTRACK` | `=y` |
| cgroup controllers | `CONFIG_CGROUPS`, `MEMCG`, `CGROUP_SCHED`, `CFS_BANDWIDTH`, `CPUSETS`, `BLK_CGROUP`, `CGROUP_PIDS/DEVICE/FREEZER`, `CGROUP_BPF` | `=y` |
| namespaces | `CONFIG_NAMESPACES`, `NET_NS`, `PID_NS`, `USER_NS`, `IPC_NS`, `UTS_NS` | `=y` |
| misc runtime | `BINFMT_MISC`, `POSIX_MQUEUE`, `KEYS`, `SECCOMP`, `BPF_SYSCALL`, `VXLAN`, `EXT4_FS` | `=y` |
| Firecracker devices | `VIRTIO_BLK`, `VIRTIO_NET`, `VIRTIO_MMIO`, `VIRTIO_PCI`, `SERIAL_8250_CONSOLE`, `PRINTK`, `ACPI`, `PCI` | `=y` |
| Bridge filtering for k8s/Docker | `CONFIG_BRIDGE_NETFILTER` | `=y` |

**Conclusion: the stock Firecracker CI 6.1 kernel is close to being able to run
Docker.** Everything essential is built in, so `nomodules` is not fatal.

Gaps to close in a dedicated **`guest-runtime`** kernel profile:

| Option | Current | Why |
|---|---|---|
| `CONFIG_NETFILTER_XT_MATCH_COMMENT` | not set | Docker/CNI insert `-m comment --comment …`; insertion fails without it |
| `CONFIG_MACVLAN` | not set | Docker `macvlan` networks; some CNI plugins |
| `CONFIG_IP_VS` | not set | Docker Swarm ingress / `ipvs` load-balancing mode |
| `CONFIG_IP_NF_RAW`, `CONFIG_IP_NF_ARPTABLES`, `CONFIG_BRIDGE_NF_EBTABLES` | not set | `iptables-legacy` raw/arp/ebtables tables used by some CNIs and legacy Docker paths |
| `CONFIG_NETFILTER_XT_MATCH_BPF` | not set | Cilium/eBPF CNIs |
| `CONFIG_SECURITY_APPARMOR` | not set | Docker AppArmor confinement (optional; document that guests run unconfined) |
| `CONFIG_SECURITY_SELINUX` | `=y` | Fedora/Rocky/AL2023 ship SELinux; keep enabled, ship policy in rootfs |
| `CONFIG_ZRAM` | not set | Optional swap/compressed memory under pressure |
| `CONFIG_DM_THIN_PROVISIONING` | not set | Only if devicemapper storage driver is required (avoid; use overlay2) |

We keep every option built-in (`=y`) so the existing `nomodules` boot arg stays
valid and we avoid building an initramfs.

## 4. Recipe model

### 4.1 Concept

A **recipe** is a declarative build input that produces one immutable artifact:

```
recipe.yaml ──> builder ──> golden-<name>-<version>.ext4
                        └─> golden-<name>-<version>.json   (metadata/checksums)
                        └─> kernel profile → vmlinux-runtime-<ver>
```

Artifacts are content-addressed, versioned, and cached. A sealed image is
registered by name so tasks can request `golden:ubuntu-24.04-docker@1.0.0`.

### 4.2 Schema (proposed, `recipes/<name>.yaml`)

```yaml
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata:
  name: ubuntu-24.04-docker
  version: 1.0.0
  description: Ubuntu 24.04 with Docker Engine, systemd, cgroup v2

spec:
  arch: [amd64, arm64]

  source:                      # how to obtain the base userspace
    type: oci                  # oci | rootfs-tar | cloud-image
    ref: docker.io/library/ubuntu:24.04

  init:
    system: systemd            # systemd | openrc | sysvinit | custom
    bootArgs:                  # appended to the translator's base args
      - systemd.unified_cgroup_hierarchy=1
      - systemd.journald.forward_to_console=1
    # init= is derived from system (or set explicitly for custom)

  kernel:
    profile: guest-runtime-6.1 # selects the vmlinux variant
    mustBoot: true

  runtime:                     # the container runtime installed *inside*
    name: docker               # docker | containerd | podman | none
    version: "27.5"
    storageDriver: overlay2
    cgroupVersion: v2
    install: script            # script | packages | mirror | none

  disk:
    rootMinSize: 4GiB          # mkfs.ext4 size floor
    dataDisk:                  # optional second writable drive for the image store
      size: 20GiB
      mount: /var/lib/docker
      fs: ext4

  network:
    guestCIDR: 172.17.0.0/16   # docker0 subnet placeholder

  provision: |                 # run inside a chroot (qemu-user for cross-arch)
    set -eux
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y ca-certificates curl iproute2 iptables nftables
    install -m0755 /tmp/get-docker.sh /root/get-docker.sh
    /root/get-docker.sh
    systemctl enable docker

  seal:                        # hygiene before mkfs
    - truncate -s 0 /etc/machine-id
    - rm -f /etc/ssh/ssh_host_*
    - rm -rf /var/cache/apt /var/lib/apt/lists/*

  health:
    - command: systemctl is-system-running --wait
    - command: docker info
      timeout: 60s

  verify:                      # boot-time assertions run by the builder/CI
    - name: kernel-has-overlayfs
      guest: grep -qw overlay /proc/filesystems
    - name: docker-bridge
      guest: ip link show docker0
```

Fields are intentionally close to the existing config vocabulary so the Go
implementation can reuse `ImagesConfig`/`ExecutorConfig` validation.

### 4.3 Recipe catalogue

| Recipe | Init | Runtime | Notes |
|---|---|---|---|
| `alpine-3.20-docker` | OpenRC | Docker | Smallest; musl; OpenRC must mount cgroups |
| `debian-12-docker` | systemd | Docker | Conservative, glibc baseline |
| `ubuntu-22.04-docker` | systemd | Docker | Matches Firecracker CI rootfs |
| `ubuntu-24.04-docker` | systemd | Docker | Current LTS, cgroup v2 default |
| `fedora-42-docker` | systemd | Docker | cgroup v2, SELinux enforcing |
| `rocky-9-docker` | systemd | Docker | RHEL-compatible, SELinux |
| `almalinux-9-docker` | systemd | Docker | RHEL-compatible, SELinux |
| `amazonlinux-2023-docker` | systemd | Docker | Firecracker-tested platform |
| `opensuse-leap-15-docker` | systemd | Docker | Btrfs-friendly, SELinux/AppArmor variants |
| `arch-docker` | systemd | Docker | Rolling; reproducible snapshot date required |
| `distroless-minimal` | tini | none | The *current* single-workload model, as a recipe |
| `busybox-scratch` | tini | none | Current scratch handling, as a recipe |

See `recipes/` for the concrete YAML and `recipes/README.md` for the schema
reference and build instructions.

## 5. Building a golden image

### 5.1 Pipeline

1. **Resolve** base (`oci` ref pinned by digest, or tarball).
2. **Extract** to a working dir (reuse `preparer.extractOCIImage`).
3. **Provision** in a chroot:
   - native arch: `chroot` directly;
   - cross arch: register `qemu-user-static` via `binfmt_misc` and chroot
     (`tonistiigi/binfmt` pattern) — the host already supports `binfmt_misc`.
   - mount `/proc`, `/sys`, `/dev`, `/dev/pts` into the chroot for package
     scripts.
4. **Install the runtime** per recipe (`runtime.install`).
5. **Configure init**: enable units/services, mount cgroups for OpenRC,
   set `docker0` CIDR, enable `net.ipv4.ip_forward=1`.
6. **Seal** (recipe `seal` steps); optionally set the rootfs read-only baseline.
7. **Size & format**: `mkfs.ext4` at `max(content*1.5, rootMinSize)` (reuse
   `createExt4ImageWithOverhead`). Record checksum + recipe digest.
8. **Smoke-boot** with the `guest-runtime` kernel and run `verify` assertions
   (machine-readable JSON result).
9. **Register**: write `<name>@<version>.json`; optionally take a **snapshot**
   of the booted VM as the fast-path artifact.
10. **Publish**: copy to each worker's rootfs dir (or an object store the
    fetcher understands).

### 5.2 Kernel build

Start from the Firecracker CI config, apply
`recipes/kernel/guest-runtime-6.1.fragment`, build `vmlinux` with `make vmlinux`
(x86_64) / `make Image` (aarch64), install as
`/usr/share/firecracker/vmlinux-runtime`. Keep options `=y`. The fragment must
be validated by a CI job that boots a recipe and runs the `verify` commands.

## 6. Execution model — how a task uses a Docker-capable VM

This is the part that needs a product decision. Three options:

### Option A — "runtime available, still one workload" (smallest change)

Golden image boots its real init, but SwarmCracker still injects the task command
as the workload. The image simply *has* `docker` on `PATH`. The service can shell
out to Docker, but there is no dockerd-by-default and no orchestration inside.
Cheap, but doesn't really deliver "VM that runs Docker".

### Option B — "VM as host + guest agent" (recommended first target)

The golden VM boots `systemd + dockerd` and runs a small **SwarmCracker guest
agent** on PID 1's supervision. The agent:

- receives the service's OCI image ref + command/env/labels over **vsock**
  (preferred; Firecracker has a vsock device) or serial,
- `docker pull` + `docker run` inside the guest,
- streams logs/health/exit status back,
- applies the task's mounts/configs/secrets by materialising them as guest
  bind mounts or `docker run` flags.

SwarmCracker's executor keeps its task state machine but, for
`runtime_mode: vm`, it provisions the golden VM and drives the agent instead of
`Initialize/Start/Delete` of a single process. This delivers real Docker-in-VM
with one host-facing task. Cost: a guest-agent protocol and lifecycle work.

### Option C — "nested swarm" (future)

Run `swarmcracker-agent` inside the golden VM and join the same (or a child)
cluster. The inner VM becomes a schedulable node; the outer executor is just a
VM lifecycle provider. Most elegant long-term, biggest blast radius (nested
networking, two schedulers, token distribution). Defer.

**Recommendation:** ship recipes + `guest-runtime` kernel + Option A first
(unblocks images and proves the kernel), then Option B.

## 7. Networking

Inside the guest:

```
eth0 (virtio-net, 192.168.127.2/24) ── docker0 (172.17.0.0/16) ── containers
```

- systemd images: enable `net.ipv4.ip_forward=1`
  (`/etc/sysctl.d/99-docker.conf`); Docker sets `FORWARD` policy itself.
- OpenRC/Alpine: an init script must mount cgroup v2 at `/sys/fs/cgroup` with
  `-o rw,relatime` (and `cgroup2` fstype), then start `dockerd`.
- The host already NATs the guest (`network.nat_enabled`); nested container
  traffic NATs twice (container → docker0 → eth0 → host MASQUERADE). Correct but
  worth documenting for MTU and conntrack limits.
- `BRIDGE_NETFILTER` is built in; ensure
  `net.bridge.bridge-nf-call-iptables=1` only if a workload needs strict bridge
  filtering (k8s); Docker itself warns when it's absent.

## 8. Storage

- **Root**: ext4, recipe-sized (4–8 GiB floor). Everything writable.
- **Data disk (recommended)**: second Firecracker drive, ext4, mounted at
  `/var/lib/docker`. Requires the translator to emit >1 drive and the
  rootfs/metadata to name the mount. Protects the base image from image-store
  growth and lets the base be made read-only later.
- **Read-only base + overlay** (firecracker-containerd style, `overlay-init`):
  strongest immutability; defer until Option B lands.
- **Snapshots**: after first successful boot, snapshot the golden VM; restore is
  the fast path. Snapshot artifacts must record the recipe+kernel digest.

## 9. Security

- Docker-in-VM is **not** a new host-privilege boundary: the guest kernel is
  still the isolation boundary; nested containers are namespaces inside the
  guest. Keep the jailer and seccomp on the Firecracker process.
- **Never** pass the host Docker socket into a guest.
- AppArmor is not enabled in the FC kernel; recipes must document that guest
  workloads are not AppArmor-confined. SELinux-capable distros keep SELinux
  enforcing with the distro's container policy.
- Golden images must be **scanned and signed**; the registry/fetcher should
  verify a digest before boot. `seal` removes host keys, machine-id, and caches.
- `nomodules` stays: no module loading in the guest means no `insmod` surface.


