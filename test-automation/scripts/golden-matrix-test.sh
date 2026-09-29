#!/usr/bin/env bash
#
# Build and boot-test golden image recipes on a KVM host.
#
# For each recipe it:
#   1. builds the artifact (forcing a fresh build),
#   2. checks the runtime and init are present in the ext4 (debugfs),
#   3. boots the artifact under Firecracker with a tap NIC and waits for the
#      container runtime to start in the guest serial log.
#
# Env overrides:
#   RECIPES   space-separated recipe names (without .yaml)
#   OUT_DIR   artifact directory (default /var/lib/firecracker/golden)
#   KERNEL    guest kernel path
#   BOOT_SECS seconds to wait for each guest (default 90)
#   FORCE=0   reuse up-to-date artifacts instead of rebuilding
#
set -uo pipefail

SWARMCRACKER=${SWARMCRACKER:-./build/swarmcracker}
RECIPE_DIR=${RECIPE_DIR:-recipes}
OUT_DIR=${OUT_DIR:-/var/lib/firecracker/golden}
KERNEL=${KERNEL:-/usr/share/firecracker/vmlinux}
FIRECRACKER=${FIRECRACKER:-firecracker}
BOOT_SECS=${BOOT_SECS:-90}
FORCE=${FORCE:-1}
TAP=${TAP:-fc-golden-tap}
RECIPES=${RECIPES:-"almalinux-9-docker ubuntu-24.04-docker debian-12-docker alpine-3.20-docker"}

LOG_DIR=$(mktemp -d /tmp/golden-matrix-XXXXXX)
mkdir -p "$OUT_DIR"
echo "logs: $LOG_DIR"

force_flag=""
[ "$FORCE" = "1" ] && force_flag="--force"

runtime_marker() {
  case "$1" in
    alpine-*) echo "Starting Docker Daemon" ;;
    *)        echo "Docker Application Container Engine|Started Docker|Started dockerd|docker.service" ;;
  esac
}

summary=""
overall=0

for name in $RECIPES; do
  recipe="$RECIPE_DIR/$name.yaml"
  build_log="$LOG_DIR/build-$name.log"
  boot_log="$LOG_DIR/boot-$name.log"

  echo "==================================================================="
  echo "== $name"

  if [ ! -f "$recipe" ]; then
    summary+="BUILD-FAIL $name (missing recipe)\n"; overall=1; continue
  fi

  # --- build ---
  if ! "$SWARMCRACKER" image build "$recipe" --output-dir "$OUT_DIR" $force_flag \
        >"$build_log" 2>&1; then
    echo "   BUILD FAILED"; tail -15 "$build_log"
    summary+="BUILD-FAIL $name\n"; overall=1; continue
  fi
  artifact=$(ls "$OUT_DIR"/golden-"$name"-*.ext4 2>/dev/null | head -1)
  if [ -z "$artifact" ]; then
    summary+="BUILD-FAIL $name (no artifact)\n"; overall=1; continue
  fi
  size=$(du -h "$artifact" | cut -f1)
  echo "   built: $artifact ($size)"

  # --- content check ---
  content="ok"
  for path in /sbin/init /usr/bin/docker; do
    if ! debugfs -R "stat $path" "$artifact" 2>/dev/null | grep -q "Inode:"; then
      content="missing:$path"
    fi
  done
  echo "   content: $content"

  # --- boot ---
  ip tuntap add dev "$TAP" mode tap 2>/dev/null || true
  ip link set "$TAP" up
  cat > "$LOG_DIR/vm-$name.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "$KERNEL",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off nomodules init=/sbin/init systemd.unified_cgroup_hierarchy=1 ip=172.16.9.2::172.16.9.1:255.255.255.0::eth0:off"
  },
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "$artifact", "is_root_device": true, "is_read_only": false}
  ],
  "network-interfaces": [
    {"iface_id": "eth0", "host_dev_name": "$TAP", "guest_mac": "02:FC:00:00:00:AA"}
  ],
  "machine-config": {"vcpu_count": 2, "mem_size_mib": 2048}
}
EOF
  rm -f "/tmp/fc-$name.sock"
  timeout "$BOOT_SECS" "$FIRECRACKER" --api-sock "/tmp/fc-$name.sock" \
      --config-file "$LOG_DIR/vm-$name.json" >"$boot_log" 2>&1 || true
  ip link del "$TAP" 2>/dev/null || true

  marker=$(runtime_marker "$name")
  if grep -aqE "$marker" "$boot_log"; then
    boot="ok"
  else
    boot="runtime-not-seen"
  fi
  welcome=$(grep -aoE "Welcome to [A-Za-z0-9 ._-]+|Reached target [A-Za-z ]+" "$boot_log" | tail -1)
  echo "   boot: $boot  ($welcome)"

  status="PASS"
  if [ "$content" != "ok" ] || [ "$boot" != "ok" ]; then status="FAIL"; overall=1; fi
  summary+="$status $name | content=$content | boot=$boot | $welcome\n"
done

echo "==================================================================="
echo -e "$summary"
echo "artifacts: $OUT_DIR"
exit $overall
