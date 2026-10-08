# SwarmCracker Golden Image Recipes

Declarative definitions for building **golden microVM images** — pre-built,
sealed, versioned rootfs artifacts that boot fast and are reused across tasks.

Two classes exist:

- **Runtime recipes** (`*-docker.yaml`): a full distro that boots its real init
  (`systemd`/OpenRC) and runs a container runtime (Docker) *inside* the VM.
- **Workload recipes** (`distroless-minimal`, `busybox-scratch`): the current
  single-container model, kept as recipes for uniformity.

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
| `spec.provision` | yes | Shell run in a chroot during build |
| `spec.seal` | no | Hygiene commands before `mkfs.ext4` |
| `spec.health` | no | Post-boot readiness checks |
| `spec.verify` | no | Boot-time assertions for CI |

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
sudo swarmcracker image build recipes/ubuntu-24.04-docker.yaml

# list / inspect
swarmcracker image list
swarmcracker image inspect ubuntu-24.04-docker@1.0.0

# use it for a service task
swarmcracker service create --name web --image nginx:alpine \
  --label swarmcracker.golden=ubuntu-24.04-docker@1.0.0
```

> `build: preparer` recipes (`busybox-scratch`, `distroless-minimal`) describe
> the legacy single-workload model and are assembled by the task-time image
> preparer; `swarmcracker image build` rejects them on purpose rather than
> producing a rootfs with no init.

## Convention

- `source.ref` must be pinned by digest for reproducible builds in CI; tags are
  accepted for local development only.
- Recipes never contain secrets. Registry credentials come from
  `images.registry_auth` / environment.
- `provision` is idempotent-ish and runs as root in a chroot; prefer the
  distro package manager over curl-pipe-bash where a package exists.
- Cross-arch builds require `qemu-user-static` + `binfmt_misc` on the builder.
