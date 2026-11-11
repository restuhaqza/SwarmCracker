# SwarmCracker Golden Image Recipes

Declarative definitions for building **golden microVM images** — pre-built,
sealed, versioned rootfs artifacts that boot fast and are reused across tasks.

Two classes exist:

- **Runtime recipes** (`*-docker.yaml`): a full distro that boots its real init
  (`systemd`/OpenRC) and runs a container runtime (Docker) *inside* the VM.
- **Workload recipes** (`distroless-minimal`, `busybox-scratch`): the current
  single-container model, kept as recipes for uniformity.
- **Reference templates** (`sandbox-*.yaml`): the sandbox set — a runtime-free
  base and one language toolchain on top of it each, with a non-root user, a
  `/workspace` mount point and warm caches. See
  [Reference templates](#reference-templates) below.

See the [Golden Images guide](../docs-site/src/content/docs/guides/golden-images.md)
for the full analysis, kernel evidence, execution-model options, and the Go work
items.

## Schema

| Field | Required | Meaning |
|---|---|---|
| `apiVersion` | yes | `swarmcracker.io/v1alpha1` |
| `kind` | yes | `GoldenImage` |
| `metadata.name` | yes | Artifact name, e.g. `ubuntu-24.04-docker` |
| `spec.build` | no | `golden` (default for real init) or `preparer` (single-workload, built at task time) |
| `metadata.version` | yes | Semver of the recipe/artifact |
| `spec.arch` | yes | Supported `amd64` / `arm64` |
| `spec.source` | yes | `oci` \| `rootfs-tar` \| `cloud-image` + pinned ref |
| `spec.init.system` | yes | `systemd` \| `openrc` \| `sysvinit` \| `custom` \| `tini` |
| `spec.init.bootArgs` | no | Appended to the translator's base boot args |
| `spec.kernel.profile` | yes | Kernel variant, e.g. `guest-runtime-6.1` |
| `spec.runtime.name` | yes | `docker` \| `containerd` \| `podman` \| `none` |
| `spec.disk.rootMinSize` | yes | Floor for `mkfs.ext4` |
| `spec.disk.dataDisk` | no | Second writable drive (image store) |
| `spec.network.guestCIDR` | no | In-guest `docker0` subnet |
| `spec.agent.protocol` | no | Guest agent protocol the image must speak; defaults to this build's minimum |
| `spec.agent.version` | no | Guest agent release the image must contain; the build verifies it against the agent that answers |
| `spec.pin` | no | Host software the template requires: `firecracker` version + `kernel` SHA-256. Both required when the block is present |
| `spec.provision` | yes | Shell run in a chroot during build |
| `spec.seal` | no | Hygiene commands before `mkfs.ext4` |
| `spec.health` | no | Post-boot readiness checks |
| `spec.verify` | no | Boot-time assertions for CI |

## Reference templates

`recipes/sandbox-*.yaml` are the reference templates of the Agent Sandbox work
(#126, SBX-044): the sealed, versioned starting points a node builds once and
then forks. They are deliberately runtime-free (`spec.runtime.name: none` — a
sandbox runs the guest agent's operations, not a container runtime), and each
provisions the same contract:

- a non-root user (uid `1000`, `sandbox`) that a sandbox command runs as,
- a `/workspace` mount point owned by that user,
- a warm cache, so a first install inside a sandbox does not start at the
  mirror.

| Template | Toolchain | Root disk | Warm cache |
|---|---|---|---|
| `sandbox-base@1.0.0` | Alpine 3.20, OpenRC, bash/coreutils/findutils/grep/sed/git/curl/tar/gzip/xz/openssh-client/iproute2/iputils/procps | 2 GiB | apk packages and index |
| `sandbox-python@1.0.0` | all of the above, plus Python 3.12, pip, setuptools, virtualenv | 3 GiB | apk, compiled stdlib bytecode, a wheelhouse at `/opt/python/wheels` |
| `sandbox-node@1.0.0` | all of the above, plus Node.js 20 and npm 10 | 3 GiB | apk, and the npm cache of the `sandbox` user |

Each `spec.verify` block is the recipe's own claim about the image, in a form a
boot can execute: the toolchain runs, the caches answer without their index, the
sandbox user is uid 1000 and can write `/workspace`.

### Proven on a KVM node

Built with `swarmcracker template build --force` on Firecracker v1.15.1 with the
`guest-runtime-6.1` kernel (`/usr/share/firecracker/vmlinux-runtime`), twice per
recipe from the same file:

| Template | Build | Artifact (apparent / on disk) | Recipe digest, both builds | Agent recorded |
|---|---|---|---|---|
| `sandbox-base@1.0.0` | 32 s / 42 s | 2048 MiB / 132 MiB | `sha256:fc014cf0f734d347b4a514eed14c042b89e13f9ca80269c3d950cebfad622239` | protocol 2, caps `[hello exec put get metrics prepare]` |
| `sandbox-python@1.0.0` | 59 s / 70 s | 3072 MiB / 242 MiB | `sha256:a4799b950e84edc189f1f7a7a86f53dcc5de9f9adad18328c8d23e7c4485fdaf` | same |
| `sandbox-node@1.0.0` | 62 s / 55 s | 3072 MiB / 248 MiB | `sha256:c519aa2b77886c9eeffdcb4120d7f167b87e06bf9a931ca40b099c95b91703e9` | same |

Each artifact was also booted again and its own `spec.verify` block run inside
the guest through the agent: base 4/4, python 7/7, node 6/6 — including the
offline checks (`pip download --no-index` from the wheelhouse, `npm install
--offline` from the cache) that prove the caches are warm rather than merely
present.

The recipe digest is identical between the two builds of a recipe; the artifact's
own SHA-256 is not, because the packer and the provisioning chroot stamp
build-time metadata into the image. That is what `pkg/golden/manifest.go`
documents: the digest is the template's identity, computed before the build, not
a byte-for-byte promise about the image. The artifact's apparent size is its
ext4 size; on disk it is sparse.

Build the set on a KVM host — a template build boots what it just sealed and
holds the guest agent to the v2 handshake, which needs KVM:

```bash
sudo swarmcracker template build recipes/sandbox-base.yaml
sudo swarmcracker template build recipes/sandbox-python.yaml
sudo swarmcracker template build recipes/sandbox-node.yaml
```

`RECIPES="sandbox-base sandbox-python sandbox-node" FORCE=0 \
test-automation/scripts/golden-matrix-test.sh` runs the same three and checks the
manifest each build wrote.

### The host pin, and where it lets a recipe be built

Every reference recipe declares `spec.pin`: the host software the template is
valid against. Both values are read from the host that builds it, not guessed:

```bash
firecracker --version                              # → v1.15.1
sha256sum /usr/share/firecracker/vmlinux-runtime   # → the kernel the guest-runtime-6.1 profile resolves to
```

A build on any other Firecracker or kernel fails with `template_pin_mismatch`
before it packs anything, rather than recording a template no other node could
resume. That is the point of the pin, and it has one consequence to keep in
mind: **a pinned recipe builds only on the host software it names.** Regenerate
both values whenever that host's Firecracker or its `guest-runtime-6.1` kernel
moves, and update all three recipes together —
`pkg/golden/reference_recipes_test.go` fails when they disagree. A maintainer who
would rather the set build on any host can drop the block: the manifest still
records the pin the build observed.

Because a template build needs KVM and `ubuntu-latest` runners have no
`/dev/kvm`, these recipes are not built by the PR CI; see
[Reference templates and where they can be built](../docs-site/src/content/docs/guides/golden-images.md#44-reference-templates-and-where-they-can-be-built-sbx-044)
for what covers them without KVM and for the two shapes a nightly could take.

## Kernel requirement

All runtime recipes assume the **`guest-runtime`** kernel profile:

```bash
# apply the fragment to the Firecracker CI config
cp recipes/kernel/guest-runtime-6.1.fragment /path/to/linux-6.1/
cd linux-6.1
./scripts/kconfig/merge_config.sh -m \
    resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config \
    guest-runtime-6.1.fragment
make olddefconfig
make -j"$(nproc)" vmlinux          # aarch64: make Image
install -m0644 vmlinux /usr/share/firecracker/vmlinux-runtime
```

The fragment adds the options the stock CI kernel is missing for Docker
(`xt_comment`, `macvlan`, `ip_vs`, raw/arp/ebtables, eBPF match, optional
AppArmor). Everything stays built-in (`=y`) so the `nomodules` boot arg remains
valid and no initramfs is needed.

## Build (intended CLI)

```bash
# build the kernel profile once
sudo scripts/build-guest-runtime-kernel.sh

# build a golden image
sudo swarmcracker template build recipes/ubuntu-24.04-docker.yaml

# list / inspect
swarmcracker template list
swarmcracker template inspect ubuntu-24.04-docker@1.0.0

# use it for a service task
swarmcracker service create --name web --image nginx:alpine \
  --label swarmcracker.golden=ubuntu-24.04-docker@1.0.0
```

> `build: preparer` recipes (`busybox-scratch`, `distroless-minimal`) describe
> the legacy single-workload model and are assembled by the task-time image
> preparer; `swarmcracker template build` rejects them on purpose rather than
> producing a rootfs with no init.

## Convention

- `source.ref` must be pinned by digest for reproducible builds in CI; tags are
  accepted for local development only.
- Recipes never contain secrets. Registry credentials come from
  `images.registry_auth` / environment.
- `provision` is idempotent-ish and runs as root in a chroot; prefer the
  distro package manager over curl-pipe-bash where a package exists.
- Cross-arch builds require `qemu-user-static` + `binfmt_misc` on the builder.
