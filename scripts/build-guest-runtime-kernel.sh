#!/usr/bin/env bash
#
# build-guest-runtime-kernel.sh - build the `guest-runtime` kernel profile that
# golden image runtime recipes require (see recipes/README.md).
#
# The Firecracker CI kernel config is missing a handful of options Docker and
# common CNIs need (macvlan, `-m comment`, raw/arp/ebtables, eBPF match, IPVS).
# This script merges recipes/kernel/guest-runtime-6.1.fragment on top of the
# matching Firecracker CI guest config, builds the kernel, and installs it as
# the runtime profile.
#
# Env overrides:
#   KERNEL_VERSION  Linux version to build          (default 6.1.188)
#   ARCH            x86_64 | aarch64                (default: host uname -m)
#   OUTPUT          installed vmlinux path          (default /usr/share/firecracker/vmlinux-runtime)
#   FIRECRACKER_REF Firecracker git ref for configs (default main)
#   FRAGMENT        kernel fragment to merge        (default <repo>/recipes/kernel/guest-runtime-6.1.fragment)
#   WORKDIR         build directory                 (default /usr/src)
#   JOBS            parallel make jobs              (default: nproc)
#   FORCE=1         rebuild even if OUTPUT exists
#
# Requires root (installs into /usr/share/firecracker). Build deps on
# Debian/Ubuntu: gcc make flex bison bc libelf-dev libssl-dev dwarves xz-utils.
#
set -euo pipefail

KERNEL_VERSION=${KERNEL_VERSION:-6.1.188}
ARCH=${ARCH:-$(uname -m)}
OUTPUT=${OUTPUT:-/usr/share/firecracker/vmlinux-runtime}
FIRECRACKER_REF=${FIRECRACKER_REF:-main}
WORKDIR=${WORKDIR:-/usr/src}
JOBS=${JOBS:-$(nproc 2>/dev/null || echo 2)}
FORCE=${FORCE:-0}

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
FRAGMENT=${FRAGMENT:-"$SCRIPT_DIR/../recipes/kernel/guest-runtime-6.1.fragment"}

log()  { printf '== %s\n' "$*"; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = "Linux" ] || die "Firecracker kernels must be built on Linux"
[ "$(id -u)" -eq 0 ] || die "must run as root (installs into $(dirname "$OUTPUT"))"

case "$ARCH" in
  x86_64|aarch64) ;;
  *) die "unsupported ARCH=$ARCH (expected x86_64 or aarch64)" ;;
esac

[ -f "$FRAGMENT" ] || die "kernel fragment not found: $FRAGMENT"

# Firecracker CI publishes configs per kernel series (e.g. 6.1, 6.18).
SERIES=${KERNEL_VERSION%.*}
CI_CONFIG="microvm-kernel-ci-${ARCH}-${SERIES}.config"
CI_URL="https://raw.githubusercontent.com/firecracker-microvm/firecracker/${FIRECRACKER_REF}/resources/guest_configs/${CI_CONFIG}"

# kernel.org lays 6.x tarballs out under v6.x/.
TARBALL="linux-${KERNEL_VERSION}.tar.xz"
TAR_URL="https://cdn.kernel.org/pub/linux/kernel/v${SERIES%%.*}.x/${TARBALL}"

# x86 builds a bare `vmlinux`; arm64 builds `Image`.
if [ "$ARCH" = "x86_64" ]; then
  MAKE_TARGET=vmlinux
else
  MAKE_TARGET=Image
fi

if [ "$FORCE" != "1" ] && [ -f "$OUTPUT" ]; then
  # grep -aq directly on the binary: a `strings | grep -q` pipeline trips
  # `set -o pipefail` because grep -q exits early and strings gets SIGPIPE.
  if grep -aq "Linux version ${KERNEL_VERSION} " "$OUTPUT"; then
    log "$OUTPUT already provides guest-runtime ${KERNEL_VERSION}; nothing to do (FORCE=1 to rebuild)"
    exit 0
  fi
fi

for tool in gcc make flex bison bc curl tar xz; do
  command -v "$tool" >/dev/null 2>&1 || die "missing build tool: $tool"
done

mkdir -p "$WORKDIR"
cd "$WORKDIR"

LINUX_DIR="$WORKDIR/linux-${KERNEL_VERSION}"
if [ ! -d "$LINUX_DIR" ]; then
  if [ ! -f "$TARBALL" ]; then
    log "downloading $TARBALL"
    curl -fL --retry 3 -o "$TARBALL" "$TAR_URL"
  fi
  log "extracting $TARBALL"
  tar -xf "$TARBALL"
fi
cd "$LINUX_DIR"

log "fetching Firecracker CI base config ($CI_CONFIG @ $FIRECRACKER_REF)"
curl -fL --retry 3 -o .config.ci "$CI_URL" \
  || die "could not fetch $CI_URL (is series $SERIES published in the Firecracker repo?)"

log "merging $(basename "$FRAGMENT")"
./scripts/kconfig/merge_config.sh -m .config.ci "$FRAGMENT" >/dev/null
make olddefconfig >/dev/null

# Fail loudly if the fragment did not survive olddefconfig.
for sym in CONFIG_MACVLAN CONFIG_NETFILTER_XT_MATCH_COMMENT CONFIG_IP_NF_RAW; do
  grep -q "^${sym}=y$" .config || die "fragment symbol $sym missing from resulting .config"
done

log "building $MAKE_TARGET with $JOBS jobs"
make -j"$JOBS" "$MAKE_TARGET"

log "installing $OUTPUT"
install -D -m0644 "$MAKE_TARGET" "$OUTPUT"

log "done: $OUTPUT ($(du -h "$OUTPUT" | cut -f1), kernel ${KERNEL_VERSION})"
grep -aoE "Linux version [0-9][^ ]*" "$OUTPUT" | head -1 || true
