#!/usr/bin/env bash
#
# cluster-lab.sh — reproducible multi-node SwarmCracker lab.
#
# Creates N nested Ubuntu VMs on a single KVM/libvirt host, provisions the
# SwarmCracker executor (Firecracker + kernel + rootfs + CNI) on each, forms a
# SwarmKit cluster with the VXLAN overlay enabled, and smoke-tests cross-host
# microVM networking.
#
# Usage:
#   test-automation/multinode/cluster-lab.sh <command> [args]
#
# Commands:
#   build              Build SwarmCracker linux binaries into $LAB_BIN
#   image              Ensure the Ubuntu cloud base image is present
#   create [N]         Create N nested VMs (default $LAB_NODES)
#   provision          Push runtime assets and configure every VM
#   cluster            Init the manager and join the workers (VXLAN enabled)
#   test               Deploy a replicated service and test cross-host traffic
#   status             Show cluster nodes, services and microVMs
#   ssh <n>            Open a shell on node <n>
#   destroy            Destroy the VMs and the seed server
#   up [N]             build + image + create + provision + cluster + status
#   down               Alias for destroy
#
# Common environment overrides:
#   LAB_PREFIX=sc-lab      VM name prefix
#   LAB_NODES=2            default node count
#   LAB_NET=default        libvirt network to attach the VMs to
#   LAB_CPUS=2 LAB_MEM=2048 LAB_DISK=24G
#   LAB_ROOT=/var/lib/swarmcracker-lab
#   LAB_IMAGE=/path/to/noble-server-cloudimg-amd64.img
#   LAB_BIN=/path/to/prebuilt/binaries
#   LAB_SUBNET=192.168.127.0/24   microVM overlay subnet
#   LAB_BRIDGE=swarm-br0          microVM bridge name
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

LAB_PREFIX="${LAB_PREFIX:-sc-lab}"
LAB_NODES="${LAB_NODES:-2}"
LAB_NET="${LAB_NET:-default}"
LAB_CPUS="${LAB_CPUS:-2}"
LAB_MEM="${LAB_MEM:-2048}"
LAB_DISK="${LAB_DISK:-24G}"
LAB_ROOT="${LAB_ROOT:-/var/lib/swarmcracker-lab}"
LAB_IMAGE="${LAB_IMAGE:-$LAB_ROOT/noble-server-cloudimg-amd64.img}"
LAB_IMAGE_URL="${LAB_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
LAB_BIN="${LAB_BIN:-$LAB_ROOT/bin}"
LAB_SSH_KEY="${LAB_SSH_KEY:-$LAB_ROOT/id_ed25519}"
LAB_SEED_PORT="${LAB_SEED_PORT:-8099}"
LAB_SUBNET="${LAB_SUBNET:-192.168.127.0/24}"
LAB_BRIDGE="${LAB_BRIDGE:-swarm-br0}"
LAB_SERVICE="${LAB_SERVICE:-lab-web}"
LAB_IMAGE_OCI="${LAB_IMAGE_OCI:-nginx:alpine}"

SSH_BASE=(-i "$LAB_SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=8)
VM_SSH_BASE=(-i "$LAB_SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=8)

info()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }
die()   { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

need_root() { [ "$(id -u)" -eq 0 ] || die "run as root (libvirt/KVM + network setup)"; }

node_name()   { printf '%s-node%s' "$LAB_PREFIX" "$1"; }
node_mac()    { printf '52:54:00:aa:bb:%02x' "$1"; }
# Overlay bridge IP for node N (assumes a /24 LAB_SUBNET, e.g. 192.168.127.0/24).
node_bridge_ip() {
  local base="${LAB_SUBNET%/*}"; base="${base%.*}"
  printf '%s.%s/24' "$base" "$1"
}

###############################################################################
# build / image
###############################################################################

cmd_build() {
  info "Building SwarmCracker binaries into $LAB_BIN"
  mkdir -p "$LAB_BIN"
  ( cd "$REPO_ROOT"
    export CGO_ENABLED=0
    local b
    for b in swarmcracker swarmd-firecracker swarmcracker-agent swarmctl swarmcracker-cni; do
      go build -o "$LAB_BIN/$b" "./cmd/$b"
      printf '  built %s\n' "$b"
    done
  )
}

cmd_image() {
  mkdir -p "$LAB_ROOT"
  if [ -s "$LAB_IMAGE" ]; then
    info "base image present: $LAB_IMAGE"
    return
  fi
  info "downloading base image to $LAB_IMAGE"
  curl -fL --retry 3 -o "$LAB_IMAGE.part" "$LAB_IMAGE_URL"
  mv "$LAB_IMAGE.part" "$LAB_IMAGE"
}

###############################################################################
# create
###############################################################################

net_host_ip() {
  virsh net-dumpxml "$LAB_NET" 2>/dev/null \
    | grep -oE "address='[0-9.]+'" | head -1 | cut -d"'" -f2
}
net_cidr() {
  virsh net-dumpxml "$LAB_NET" 2>/dev/null \
    | grep -oE "address='[0-9.]+' netmask='[0-9.]+'" | head -1
}

seed_server_start() {
  local ip; ip="$(net_host_ip)"
  [ -n "$ip" ] || die "could not determine host IP for libvirt network '$LAB_NET'"
  local netmask; netmask="$(virsh net-dumpxml "$LAB_NET" | grep -oE "netmask='[0-9.]+'" | head -1 | cut -d"'" -f2)"
  info "starting cloud-init seed server at http://$ip:$LAB_SEED_PORT"
  systemctl stop swarmcracker-lab-seed.service >/dev/null 2>&1 || true
  systemd-run --unit=swarmcracker-lab-seed --working-directory="$LAB_ROOT/http" \
    python3 -m http.server "$LAB_SEED_PORT" --bind "$ip" >/dev/null
  # allow the guest network to reach the seed server
  if ! iptables -C INPUT -s "$ip/$netmask" -p tcp --dport "$LAB_SEED_PORT" -j ACCEPT 2>/dev/null; then
    iptables -I INPUT -s "$ip/$netmask" -p tcp --dport "$LAB_SEED_PORT" -j ACCEPT 2>/dev/null || true
  fi
}

seed_for_node() {
  local idx="$1" ip="$2" pubkey="$3" name; name="$(node_name "$idx")"
  local dir="$LAB_ROOT/http/$name"
  mkdir -p "$dir"
  cat > "$dir/user-data" <<EOF
#cloud-config
hostname: $name
manage_etc_hosts: true
users:
  - name: root
    ssh_authorized_keys:
      - $pubkey
    lock_passwd: false
  - name: ubuntu
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $pubkey
disable_root: false
ssh_pwauth: false
package_update: true
packages:
  - iproute2
  - iptables
  - kmod
  - dnsmasq
  - e2fsprogs
  - curl
  - ca-certificates
  - psmisc
  - netcat-openbsd
write_files:
  - path: /etc/modprobe.d/kvm-nested.conf
    content: |
      options kvm_intel nested=1
      options kvm_amd nested=1
runcmd:
  - [ sh, -c, "modprobe kvm_intel nested=1 || modprobe kvm_amd nested=1 || true" ]
  - [ sh, -c, "systemctl enable --now serial-getty@ttyS0.service || true" ]
  - [ sh, -c, "touch /root/provisioned" ]
EOF
  cat > "$dir/meta-data" <<EOF
instance-id: $name
local-hostname: $name
EOF
}

# patch_sysinfo <xml-file> <serial>
patch_sysinfo() {
  python3 - "$1" "$2" <<'PY'
import sys, re
path, serial = sys.argv[1], sys.argv[2]
xml = open(path).read()
if "smbios mode=" not in xml:
    xml = xml.replace("<os>", '<os>\n    <smbios mode="sysinfo"/>', 1)
repl = '<entry name="serial">%s</entry>' % serial
pat = r'<entry name=[\'"]serial[\'"]>.*?</entry>'
if re.search(pat, xml, flags=re.S):
    xml = re.sub(pat, repl, xml, flags=re.S)
elif "<sysinfo" in xml:
    xml = xml.replace("</system>", repl + "\n    </system>", 1)
else:
    block = ('  <sysinfo type="smbios">\n    <system>\n      %s\n    </system>\n  </sysinfo>\n' % repl)
    xml = xml.replace("</domain>", block + "</domain>", 1)
assert serial in xml
open(path, "w").write(xml)
PY
}

cmd_create() {
  need_root
  local n="${1:-$LAB_NODES}"
  [ -s "$LAB_IMAGE" ] || die "base image missing; run: $0 image"
  command -v virt-install >/dev/null || die "virt-install not found"

  mkdir -p "$LAB_ROOT/vms" "$LAB_ROOT/http"
  [ -f "$LAB_SSH_KEY" ] || ssh-keygen -t ed25519 -N '' -f "$LAB_SSH_KEY" -C "$LAB_PREFIX" >/dev/null
  local pubkey; pubkey="$(cat "$LAB_SSH_KEY.pub")"

  virsh net-start "$LAB_NET" >/dev/null 2>&1 || true
  virsh net-autostart "$LAB_NET" >/dev/null 2>&1 || true

  seed_server_start
  local ip; ip="$(net_host_ip)"

  local i name mac base qcow xml
  for i in $(seq 1 "$n"); do
    name="$(node_name "$i")"
    mac="$(node_mac "$i")"
    qcow="$LAB_ROOT/vms/$name.qcow2"
    if virsh dominfo "$name" >/dev/null 2>&1; then
      warn "$name already exists; skipping"
      continue
    fi
    info "creating $name ($mac)"
    qemu-img create -f qcow2 -F qcow2 -b "$LAB_IMAGE" "$qcow" "$LAB_DISK" >/dev/null
    seed_for_node "$i" "$ip" "$pubkey"
    xml="$LAB_ROOT/vms/$name.xml"
    virt-install --connect qemu:///system \
      --name "$name" --memory "$LAB_MEM" --vcpus "$LAB_CPUS" \
      --cpu host-passthrough --osinfo detect=on,require=off \
      --import --disk "path=$qcow,format=qcow2,bus=virtio" \
      --network "network=$LAB_NET,model=virtio,mac=$mac" \
      --graphics none --console pty,target_type=serial \
      --print-xml > "$xml"
    patch_sysinfo "$xml" "ds=nocloud-net;s=http://$ip:$LAB_SEED_PORT/$name/"
    virsh define "$xml" >/dev/null
    virsh start "$name" >/dev/null
  done
  info "waiting for guests to boot and cloud-init to finish (SSH access)"
  wait_ssh_all "$n"
}

node_ip() {
  local name; name="$(node_name "$1")"
  local mac; mac="$(node_mac "$1")"
  virsh net-dhcp-leases "$LAB_NET" 2>/dev/null \
    | grep -i "$mac" | awk '{print $5}' | cut -d/ -f1 | tail -1
}

wait_ssh_all() {
  local n="$1" i ip ok
  for _ in $(seq 1 60); do
    ok=1
    for i in $(seq 1 "$n"); do
      ip="$(node_ip "$i" || true)"
      if [ -n "$ip" ] && ssh "${VM_SSH_BASE[@]}" "root@$ip" "test -f /root/provisioned" 2>/dev/null; then :; else ok=0; fi
    done
    [ "$ok" = 1 ] && { info "all $n guests ready"; return; }
    sleep 10
  done
  die "timed out waiting for guests"
}

###############################################################################
# provision
###############################################################################

node_ssh() { # <idx> <remote-cmd>
  local ip; ip="$(node_ip "$1")"
  [ -n "$ip" ] || die "no IP for node $1"
  ssh "${VM_SSH_BASE[@]}" "root@$ip" "$2"
}

cmd_provision() {
  need_root
  [ -n "$(node_ip 1 || true)" ] || die "no nodes found; run: $0 create"
  local i ip
  for i in $(seq 1 "$LAB_NODES"); do
    ip="$(node_ip "$i")"
    info "provisioning node $i ($ip)"
    push_assets "$ip"
  done
}

push_assets() {
  local ip="$1"
  local scp=(scp -i "$LAB_SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)
  local sshc=(ssh "${VM_SSH_BASE[@]}")

  "${sshc[@]}" "root@$ip" "mkdir -p /usr/local/bin /opt/cni/bin /usr/share/firecracker /var/lib/firecracker/rootfs /var/lib/firecracker/golden /etc/cni/net.d /var/lib/cni /etc/swarmcracker /var/lib/swarmcracker /var/cache/swarmcracker"

  # Host runtime assets (populate with `swarmcracker setup install ...` on the host)
  [ -x /usr/local/bin/firecracker ] || die "missing /usr/local/bin/firecracker on host"
  [ -s /usr/share/firecracker/vmlinux ] || die "missing /usr/share/firecracker/vmlinux on host"
  [ -d /opt/cni/bin ] || die "missing /opt/cni/bin on host"

  rsync -a --sparse -e "ssh ${VM_SSH_BASE[*]}" /opt/cni/bin/ "root@$ip":/opt/cni/bin/
  rsync -a --sparse -e "ssh ${VM_SSH_BASE[*]}" \
    "$LAB_BIN/swarmcracker" "$LAB_BIN/swarmd-firecracker" \
    "$LAB_BIN/swarmcracker-agent" "$LAB_BIN/swarmctl" \
    "root@$ip":/usr/local/bin/
  rsync -a --sparse -e "ssh ${VM_SSH_BASE[*]}" "$LAB_BIN/swarmcracker-cni" "root@$ip":/opt/cni/bin/
  rsync -a --sparse -e "ssh ${VM_SSH_BASE[*]}" /usr/local/bin/firecracker /usr/local/bin/jailer "root@$ip":/usr/local/bin/ 2>/dev/null || \
    rsync -a --sparse -e "ssh ${VM_SSH_BASE[*]}" /usr/local/bin/firecracker "root@$ip":/usr/local/bin/
  rsync -a --sparse -e "ssh ${VM_SSH_BASE[*]}" /usr/share/firecracker/vmlinux "root@$ip":/usr/share/firecracker/vmlinux

  # Base rootfs + any golden images (best effort)
  shopt -s nullglob
  local extra=()
  local f
  for f in /var/lib/firecracker/rootfs/*.ext4; do extra+=("$f"); done
  for f in /var/lib/firecracker/golden/*; do extra+=("$f"); done
  shopt -u nullglob
  if [ "${#extra[@]}" -gt 0 ]; then
    rsync -a --sparse -e "ssh ${VM_SSH_BASE[*]}" --relative "${extra[@]}" "root@$ip":/ 2>/dev/null || \
      warn "some rootfs/golden assets could not be copied to $ip"
  fi

  "${sshc[@]}" "root@$ip" "chmod +x /usr/local/bin/swarmcracker /usr/local/bin/swarmd-firecracker /usr/local/bin/swarmcracker-agent /usr/local/bin/swarmctl /usr/local/bin/firecracker /usr/local/bin/jailer /opt/cni/bin/swarmcracker-cni 2>/dev/null; firecracker --version | head -1"
}

###############################################################################
# cluster
###############################################################################

cmd_cluster() {
  need_root
  local n="$LAB_NODES"
  local i ip
  local ips=()
  for i in $(seq 1 "$n"); do
    ip="$(node_ip "$i")"
    [ -n "$ip" ] || die "no IP for node $i"
    ips+=("$ip")
  done
  peers_for() { # all other transport IPs
    local self="$1" out=""
    local k
    for k in "${!ips[@]}"; do
      [ "${ips[$k]}" = "$self" ] && continue
      out="${out:+$out,}${ips[$k]}"
    done
    printf '%s' "$out"
  }

  info "initializing manager on node 1 (${ips[0]})"
  node_ssh 1 "modprobe kvm_intel nested=1 || true; modprobe tun; modprobe br_netfilter; systemctl stop swarmcracker-manager 2>/dev/null; rm -rf /var/lib/swarmkit; swarmcracker cluster init --advertise-addr ${ips[0]}:4242 --bridge-ip $(node_bridge_ip 1) --subnet $LAB_SUBNET --vxlan-enabled --vxlan-peers $(peers_for "${ips[0]}") --enable-cni" >/dev/null 2>&1 \
    || die "cluster init failed on node 1"

  local token
  token="$(node_ssh 1 "grep WORKER_TOKEN /var/lib/swarmkit/join-tokens.txt | cut -d= -f2")"
  [ -n "$token" ] || die "could not read worker join token"

  for i in $(seq 2 "$n"); do
    info "joining node $i (${ips[$((i-1))]})"
    node_ssh "$i" "modprobe kvm_intel nested=1 || true; modprobe tun; modprobe br_netfilter; systemctl stop swarmcracker-worker 2>/dev/null; rm -rf /var/lib/swarmkit; swarmcracker cluster join ${ips[0]}:4242 --token $token --advertise-addr ${ips[$((i-1))]}:4242 --bridge-ip $(node_bridge_ip "$i") --subnet $LAB_SUBNET --vxlan-enabled --vxlan-peers $(peers_for "${ips[$((i-1))]}") --enable-cni" >/dev/null 2>&1 \
      || die "cluster join failed on node $i"
  done
  info "cluster formed"
}

###############################################################################
# test
###############################################################################

cmd_test() {
  need_root
  local n="$LAB_NODES"
  info "deploying $n replicas of $LAB_IMAGE_OCI"
  node_ssh 1 "swarmcracker service rm $LAB_SERVICE --force >/dev/null 2>&1 || true"
  node_ssh 1 "swarmcracker service create --name $LAB_SERVICE --image $LAB_IMAGE_OCI --replicas $n" >/dev/null

  local i
  for i in $(seq 1 30); do
    local running
    running="$(node_ssh 1 "swarmcracker service ps $LAB_SERVICE 2>/dev/null | grep -c RUNNING" || true)"
    [ "${running:-0}" -ge "$n" ] && break
    sleep 5
  done
  node_ssh 1 "swarmcracker service ps $LAB_SERVICE"

  # collect one microVM IP per node
  local ips=() svc ip
  for i in $(seq 1 "$n"); do
    ip="$(node_ssh "$i" "journalctl -u swarmcracker-manager -u swarmcracker-worker -o cat --no-pager 2>/dev/null | grep -aoE '\"ip\":\"[0-9.]+\"' | tail -1 | grep -oE '[0-9.]+'" || true)"
    ips+=("$ip")
  done

  echo
  info "cross-host reachability matrix"
  local a b fromip target ping_code http_code
  for a in $(seq 1 "$n"); do
    fromip="$(node_ip "$a")"
    for b in $(seq 1 "$n"); do
      target="${ips[$((b-1))]}"
      [ -n "$target" ] || continue
      ping_code="$(node_ssh "$a" "ping -c3 -W2 -q $target >/dev/null 2>&1 && echo OK || echo FAIL")"
      http_code="$(node_ssh "$a" "curl -s --max-time 8 -o /dev/null -w '%{http_code}' http://$target/ 2>/dev/null")"
      printf '  node%s -> node%s microVM %-15s ping=%-5s http=%s\n' "$a" "$b" "$target" "$ping_code" "$http_code"
    done
  done
}

###############################################################################
# status / ssh / destroy
###############################################################################

cmd_status() {
  need_root
  local i
  echo "=== nodes ==="
  node_ssh 1 "swarmcracker node ls" 2>/dev/null || warn "manager not reachable"
  echo "=== services ==="
  node_ssh 1 "swarmcracker service ls" 2>/dev/null || true
  for i in $(seq 1 "$LAB_NODES"); do
    echo "=== node $i ($(node_ip "$i" 2>/dev/null)) microVMs ==="
    node_ssh "$i" "swarmcracker vm list" 2>/dev/null | head -8 || true
  done
}

cmd_ssh() {
  local i="${1:-1}"
  local ip; ip="$(node_ip "$i")"
  [ -n "$ip" ] || die "no IP for node $i"
  exec ssh -i "$LAB_SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "root@$ip"
}

cmd_destroy() {
  need_root
  local i name
  for i in $(seq 1 20); do
    name="$(node_name "$i")"
    virsh dominfo "$name" >/dev/null 2>&1 || continue
    info "destroying $name"
    virsh destroy "$name" >/dev/null 2>&1 || true
    virsh undefine "$name" >/dev/null 2>&1 || true
    rm -f "$LAB_ROOT/vms/$name.xml" "$LAB_ROOT/vms/$name.qcow2"
  done
  systemctl stop swarmcracker-lab-seed.service >/dev/null 2>&1 || true
  rm -rf "$LAB_ROOT/http"
  info "lab destroyed (base image and binaries kept under $LAB_ROOT)"
}

###############################################################################
# dispatch
###############################################################################

usage() { sed -n '2,45p' "$0" | sed 's/^# \{0,1\}//'; }

case "${1:-}" in
  build)      cmd_build ;;
  image)      cmd_image ;;
  create)     shift; cmd_create "${1:-}" ;;
  provision)  cmd_provision ;;
  cluster)    cmd_cluster ;;
  test)       cmd_test ;;
  status)     cmd_status ;;
  ssh)        shift; cmd_ssh "${1:-1}" ;;
  destroy|down) cmd_destroy ;;
  up)         shift; LAB_NODES="${1:-$LAB_NODES}"
              cmd_build; cmd_image; cmd_create "$LAB_NODES"; cmd_provision; cmd_cluster; cmd_status ;;
  ""|-h|--help|help) usage ;;
  *)          die "unknown command: $1 (try --help)" ;;
esac
