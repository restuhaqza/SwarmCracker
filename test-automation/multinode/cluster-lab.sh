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
#   ingress            E2E the ingress routing mesh (fan-out, failover, UDP, cleanup)
#   three-tier         E2E a web + api + db app: publish, secrets, health, service DNS
#   autoscale          E2E the autoscaler: scale up on CPU, scale in, and a paused service
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
LAB_DB_IMAGE="${LAB_DB_IMAGE:-alpine:3.20}"

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

  # Base rootfs + any golden images (best effort).
  #
  # A rootfs is seeded with its guest-version marker, because the daemon trusts a
  # cached image whose marker matches its own guest build. Copying the image
  # alone leaves whatever marker the guest already had, so a stale image can look
  # current and the guest then runs an old init wrapper (no sc.argv support, so
  # --command/--args is silently dropped). A marker the seed did not provide is
  # removed for the same reason.
  shopt -s nullglob
  local extra=()
  local markerless=""
  local f
  for f in /var/lib/firecracker/rootfs/*.ext4; do
    extra+=("$f")
    if [ -f "$f.guest-version" ]; then
      extra+=("$f.guest-version")
    else
      markerless="$markerless $f.guest-version"
    fi
  done
  for f in /var/lib/firecracker/golden/*; do extra+=("$f"); done
  shopt -u nullglob
  if [ "${#extra[@]}" -gt 0 ]; then
    rsync -a --sparse -e "ssh ${VM_SSH_BASE[*]}" --relative "${extra[@]}" "root@$ip":/ 2>/dev/null || \
      warn "some rootfs/golden assets could not be copied to $ip"
  fi
  if [ -n "$markerless" ]; then
    # shellcheck disable=SC2086 # a space-separated path list, built above
    "${sshc[@]}" "root@$ip" "rm -f$markerless" 2>/dev/null || true
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
  # Re-clustering must be idempotent: an existing manager leaves a systemd unit
  # that makes `cluster init` refuse. Deinit first (and clean the bridge/VXLAN
  # it created); a node without a manager is a no-op.
  node_ssh 1 "swarmcracker cluster deinit --purge --cleanup-network --force >/dev/null 2>&1 || true"
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
# ingress routing mesh e2e
###############################################################################

# cmd_ingress exercises the ingress routing mesh end to end: it publishes a
# service and checks that EVERY node serves it (fan-out), that scaling to a
# single replica keeps every node serving (failover), that UDP datagrams are
# DNAT'd on every node, and that removing the service clears the rules.
cmd_ingress() {
  need_root
  [ -n "$(node_ip 1 || true)" ] || die "no nodes found; run: $0 create"
  local n="$LAB_NODES"
  local port="${LAB_INGRESS_PORT:-18090}"
  local udp_port="${LAB_INGRESS_UDP_PORT:-18096}"
  local svc="${LAB_INGRESS_SERVICE:-lab-ingress}"
  local svc_udp="${svc}-udp"
  local rc=0 i ip code

  wait_running() { # <service> <count>
    local j r
    for j in $(seq 1 40); do
      r="$(node_ssh 1 "swarmcracker service ps $1 2>/dev/null | grep -c RUNNING" || true)"
      [ "${r:-0}" -ge "$2" ] && return 0
      sleep 5
    done
    return 1
  }

  serve_all() { # <port> <label>
    local j code
    for j in $(seq 1 "$n"); do
      ip="$(node_ip "$j")"
      code="$(curl -s --max-time 8 -o /dev/null -w '%{http_code}' "http://$ip:$port/" 2>/dev/null)"
      printf '  %s node%s (%s):%s -> %s\n' "$2" "$j" "$ip" "$port" "$code"
      [ "$code" = "200" ] || rc=1
    done
  }

  info "TCP ingress: $n replicas of $LAB_IMAGE_OCI published on $port"
  node_ssh 1 "swarmcracker service rm $svc --force >/dev/null 2>&1 || true"
  node_ssh 1 "swarmcracker service create --name $svc --image $LAB_IMAGE_OCI --replicas $n --publish $port:80" >/dev/null
  wait_running "$svc" "$n" || die "replicas did not become RUNNING"

  info "fan-out: every node must serve the published port"
  serve_all "$port" "fan-out"
  [ "$rc" = 0 ] || die "ingress fan-out failed: not every node returned 200"

  info "failover: scale to one replica; every node must still serve"
  node_ssh 1 "swarmcracker service scale $svc 1" >/dev/null
  wait_running "$svc" 1 || die "scale-down did not settle"
  sleep 6
  serve_all "$port" "failover"
  [ "$rc" = 0 ] || die "failover failed: not every node returned 200"

  info "UDP datapath: datagrams to :$udp_port must be DNAT'd on every node"
  if command -v nc >/dev/null 2>&1; then
    node_ssh 1 "swarmcracker service rm $svc_udp --force >/dev/null 2>&1 || true"
    node_ssh 1 "swarmcracker service create --name $svc_udp --image $LAB_IMAGE_OCI --replicas $n --publish $udp_port:80/udp" >/dev/null
    wait_running "$svc_udp" "$n" || die "UDP replicas did not become RUNNING"
    sleep 6
    local before after sent=10 j
    for j in $(seq 1 "$n"); do
      ip="$(node_ip "$j")"
      before="$(node_ssh "$j" "iptables -t nat -nvL PREROUTING | grep -E 'dpt:$udp_port' | awk '{s+=\$1} END {print s+0}'" || echo 0)"
      local k
      for k in $(seq 1 "$sent"); do printf 'u' | timeout 2 nc -u -w1 "$ip" "$udp_port" >/dev/null 2>&1 || true; done
      sleep 1
      after="$(node_ssh "$j" "iptables -t nat -nvL PREROUTING | grep -E 'dpt:$udp_port' | awk '{s+=\$1} END {print s+0}'" || echo 0)"
      printf '  node%s udp rule packets: %s -> %s (sent %s)\n' "$j" "$before" "$after" "$sent"
      [ "$after" -ge $((before + sent)) ] || rc=1
    done
    [ "$rc" = 0 ] || die "UDP datapath check failed"
  else
    warn "nc not found; skipping the UDP datapath check"
  fi

  info "cleanup: removing the services must clear ingress rules on every node"
  node_ssh 1 "swarmcracker service rm $svc --force >/dev/null 2>&1 || true"
  node_ssh 1 "swarmcracker service rm $svc_udp --force >/dev/null 2>&1 || true"
  sleep 8
  for i in $(seq 1 "$n"); do
    local left
    left="$(node_ssh "$i" "iptables -t nat -S PREROUTING | grep -c swarmcracker:ingress || true")"
    printf '  node%s leftover ingress rules: %s\n' "$i" "${left:-0}"
    [ "${left:-0}" = "0" ] || rc=1
  done
  [ "$rc" = 0 ] || die "ingress cleanup failed"

  info "ingress routing mesh e2e passed"
}

###############################################################################
# three-tier e2e: web + api + db
###############################################################################

# cmd_three_tier exercises the acceptance test this roadmap epic is defined by:
# a fresh three-tier app deployed with published ports, a secret, healthchecked
# starts and service-name DNS, with no manual IP lookups. It also covers #97
# (--command/--args) by giving the db task a command with a space in it, and it
# proves a failing healthcheck keeps a task out of RUNNING (the mechanism a
# health-gated rolling update relies on).
cmd_three_tier() {
  need_root
  [ -n "$(node_ip 1 || true)" ] || die "no nodes found; run: $0 create"
  local n="$LAB_NODES"
  local prefix="${LAB_THREE_TIER_PREFIX:-sc3t}"
  local web="${prefix}-web" api="${prefix}-api" db="${prefix}-db"
  local gate="${prefix}-gate"
  local secret="${prefix}-db-pass"
  local port="${LAB_THREE_TIER_PORT:-18080}"
  local db_pass="s3cr3t-$$"
  local rc=0 j i host task out ip code

  wait_running() { # <service> <count>
    local j r
    for j in $(seq 1 60); do
      r="$(node_ssh 1 "swarmcracker service ps $1 2>/dev/null | grep -c RUNNING" || true)"
      [ "${r:-0}" -ge "$2" ] && return 0
      sleep 5
    done
    return 1
  }

  # find_local_task prints "<node> <task-id>" for a task of <service> that runs
  # on a node of this lab, so `vm exec` can use its local vsock.
  find_local_task() { # <service>
    local svc="$1" j id
    for j in $(seq 1 "$n"); do
      for id in $(node_ssh 1 "swarmcracker service ps $svc -q 2>/dev/null" || true); do
        [ -n "$id" ] || continue
        if node_ssh "$j" "swarmcracker vm list 2>/dev/null" | awk '{print $1}' | grep -qx -- "$id"; then
          printf '%s %s\n' "$j" "$id"
          return 0
        fi
      done
    done
    return 1
  }

  info "three-tier e2e: $web (ingress + DNS) -> $api (healthchecked) -> $db (secret + --command)"
  node_ssh 1 "swarmcracker service rm $web --force >/dev/null 2>&1 || true"
  node_ssh 1 "swarmcracker service rm $api --force >/dev/null 2>&1 || true"
  node_ssh 1 "swarmcracker service rm $db --force >/dev/null 2>&1 || true"
  node_ssh 1 "swarmcracker service rm $gate --force >/dev/null 2>&1 || true"
  node_ssh 1 "swarmcracker secret rm $secret >/dev/null 2>&1 || true"

  info "secret: creating $secret on the manager"
  node_ssh 1 "printf '%s' '$db_pass' > /tmp/$secret && swarmcracker secret create $secret /tmp/$secret >/dev/null && rm -f /tmp/$secret"

  info "db: alpine, secret at /run/secrets/$secret, --command with a space in an arg"
  node_ssh 1 "swarmcracker service create --name $db --image $LAB_DB_IMAGE \
    --secret src=$secret,target=/run/secrets/$secret \
    --command=/bin/sh --args=-c --args='sleep 3600' \
    --health-cmd 'test -s /run/secrets/$secret' \
    --health-interval 2s --health-timeout 2s --health-retries 3 --health-start-period 5s" >/dev/null
  wait_running "$db" 1 || die "db never became RUNNING (secret, command or healthcheck failed)"

  info "api: healthchecked, no published port"
  node_ssh 1 "swarmcracker service create --name $api --image $LAB_IMAGE_OCI --replicas $n \
    --health-cmd 'wget -q -O /dev/null http://127.0.0.1/' \
    --health-interval 2s --health-timeout 2s --health-retries 3 --health-start-period 5s" >/dev/null
  wait_running "$api" "$n" || { node_ssh 1 "swarmcracker service ps $api"; die "api replicas did not become healthy"; }

  info "web: $n replicas published on :$port via ingress"
  node_ssh 1 "swarmcracker service create --name $web --image $LAB_IMAGE_OCI --replicas $n --publish $port:80 \
    --health-cmd 'wget -q -O /dev/null http://127.0.0.1/' \
    --health-interval 2s --health-timeout 2s --health-retries 3 --health-start-period 5s" >/dev/null
  wait_running "$web" "$n" || { node_ssh 1 "swarmcracker service ps $web"; die "web replicas did not become healthy"; }

  info "check: the published port answers on every node"
  for j in $(seq 1 "$n"); do
    ip="$(node_ip "$j")"
    code=""
    # The ingest rules are programmed by a reconciler shortly after the tasks
    # become RUNNING, so retry instead of failing the whole run on the first
    # connection refused (a failed curl must not abort under set -e either).
    for _ in $(seq 1 12); do
      code="$(curl -s --max-time 8 -o /dev/null -w '%{http_code}' "http://$ip:$port/" 2>/dev/null || true)"
      [ "$code" = "200" ] && break
      sleep 5
    done
    printf '  node%s (%s):%s -> %s\n' "$j" "$ip" "$port" "${code:-no-response}"
    [ "$code" = "200" ] || rc=1
  done

  info "check: a web guest resolves the api service by name and reaches it (no IP lookup)"
  if ! read -r host task < <(find_local_task "$web"); then
    die "could not find a web task running on a lab node"
  fi
  # The service name and its tasks.<name> alias are both part of the DNS
  # contract; the lab prefixes names to stay collision-free across runs.
  for _name in "$api" "tasks.$api"; do
    out="$(node_ssh "$host" "swarmcracker vm exec $task -- wget -q -O- http://$_name" 2>&1)" || rc=1
    if printf '%s' "$out" | grep -qi 'nginx'; then
      printf '  web task on node%s -> http://%s -> nginx page (%s bytes)\n' "$host" "$_name" "${#out}"
    else
      printf '  web task on node%s -> http://%s -> FAILED: %s\n' "$host" "$_name" "$out"
      rc=1
    fi
  done

  info "check: the secret exists inside the db guest with mode 0400 and the right value"
  if ! read -r host task < <(find_local_task "$db"); then
    die "could not find a db task running on a lab node"
  fi
  out="$(node_ssh "$host" "swarmcracker vm exec $task -- sh -c 'ls -l /run/secrets/$secret; cat /run/secrets/$secret'" 2>&1)" || rc=1
  printf '%s\n' "$out" | sed 's/^/  /'
  printf '%s' "$out" | grep -q -- '-r--------' || { printf '  secret mode is not 0400\n'; rc=1; }
  printf '%s' "$out" | grep -q -- "$db_pass" || { printf '  secret content does not match\n'; rc=1; }

  info "check: the db task runs the requested command (--command/--args), not the image entrypoint"
  out="$(node_ssh "$host" "swarmcracker vm exec $task -- cat /proc/1/cmdline" 2>/dev/null | tr '\0' ' ')" || rc=1
  printf '  pid 1: %s\n' "$out"
  printf '%s' "$out" | grep -q 'sleep 3600' || { printf '  the command override did not take effect\n'; rc=1; }

  info "check: a failing healthcheck keeps a task out of RUNNING"
  node_ssh 1 "swarmcracker service create --name $gate --image $LAB_IMAGE_OCI --replicas 1 \
    --health-cmd 'test -f /run/never-healthy' \
    --health-interval 2s --health-timeout 2s --health-retries 2 --health-start-period 1s" >/dev/null
  sleep 20
  out="$(node_ssh 1 "swarmcracker service ps $gate --no-health 2>/dev/null" || true)"
  printf '%s\n' "$out" | sed 's/^/  /'
  if printf '%s' "$out" | grep -q RUNNING; then
    printf '  gate task reached RUNNING despite a failing check\n'
    rc=1
  fi
  node_ssh 1 "swarmcracker service rm $gate --force >/dev/null 2>&1 || true"

  [ "$rc" = 0 ] || die "three-tier e2e failed"
  info "three-tier e2e passed (left running: $web, $api, $db; secret: $secret)"
}

###############################################################################
# autoscale e2e: CPU-driven scale-up, scale-in and a paused service
###############################################################################

# cmd_autoscale is the reproducible form of #49's autoscaling acceptance test,
# which until now only existed as a recorded manual run: a CPU-bound service
# must reach its maximum on its own, come back to its minimum when the load
# stops, and stay at zero after an operator scales it to zero.
#
# Every change is measured twice — the replica count the manager reports
# (`service ls`) and the manager's own counter,
# swarmcracker_service_scale_events_total{direction="up"|"down"}, read from the
# daemon's --health-addr on the manager — because the count alone cannot tell a
# load-driven scale from an operator's write, and the manual run this replaces
# was read from that counter.
#
# The series' `service` label is the service ID, not its name, so the scenario
# resolves the ID after create and reports both that service's own series and
# the sum over every service.
#
# The timing is the daemon's, not this script's: the loop reconciles every 30s,
# a scale-up reacts on the first hot sample (the second one a replica reports,
# because the first is only a baseline), a scale-down needs two consecutive cold
# samples, and the policy's cooldowns (30s up, 60s down) gate the next change.
# A scale moves one replica, so 3 -> 1 is two events and takes about two and a
# half minutes; the whole scenario takes roughly six.
cmd_autoscale() {
  need_root
  [ -n "$(node_ip 1 || true)" ] || die "no nodes found; run: $0 create"

  local n="$LAB_NODES"
  local svc="${LAB_AUTOSCALE_PREFIX:-scas}"
  local image="${LAB_AUTOSCALE_IMAGE:-$LAB_DB_IMAGE}"
  local health="${LAB_AUTOSCALE_HEALTH_ADDR:-127.0.0.1:8080}"
  local amin=1 amax=3 atarget=40
  local cooldown_up=30s cooldown_down=60s
  local up_deadline="${LAB_AUTOSCALE_UP_DEADLINE:-240}"
  local down_deadline="${LAB_AUTOSCALE_DOWN_DEADLINE:-210}"
  local pause_window=95
  local rc=0
  local svc_id="" up_before="" down_before="" up_after="" down_after=""
  local up_paused="" down_paused="" up_for_service="" down_for_service=""
  local ok host task touched expected i cur stayed

  # The workload burns the guest's CPU until /tmp/stop exists and then idles in
  # `sleep`, keeping the task alive and cold. Two properties matter:
  #
  #   - it must be hot with room to spare. The arithmetic burst is a shell
  #     builtin loop (no fork per iteration) followed by a 50ms sleep, which
  #     measured ~75% of one vCPU in alpine:3.20 here and stays over the 44%
  #     "hot" band (target 40 plus the 10% band) on a faster or slower guest.
  #   - it must yield. A bare `while :; do :; done` would still be sampled — the
  #     guest agent gets fair CPU — but every `vm exec` and every metrics poll
  #     would wait behind it, and the stop signal would only be noticed when the
  #     scheduler felt like it. The sleep gives the agent a window every burst.
  #
  # shellcheck disable=SC2016  # the guest expands these, not this script
  local burn='i=0; while [ ! -f /tmp/stop ]; do while [ "$i" -lt 100000 ]; do i=$((i+1)); done; i=0; sleep 0.05; done; exec sleep 3600'

  # service_id prints a service's full ID, which is what the scale-event series
  # is labelled with.
  service_id() { # <name>
    node_ssh 1 "swarmcracker service ls -q --filter name=$1 2>/dev/null" 2>/dev/null \
      | head -1 || true
  }

  # spec_replicas prints the replica count the manager reports, or nothing when
  # the service is gone.
  spec_replicas() { # <name>
    node_ssh 1 "swarmcracker service ls --filter name=$1 2>/dev/null" 2>/dev/null \
      | awk -v name="$1" '$2 == name { print $3; exit }' || true
  }

  # running_replicas counts the service's RUNNING tasks.
  running_replicas() { # <name>
    node_ssh 1 "swarmcracker service ps $1 2>/dev/null | grep -c RUNNING || true" 2>/dev/null || true
  }

  # scale_events sums swarmcracker_service_scale_events_total for one direction,
  # optionally narrowed to one service ID. awk always prints a number, so a
  # series the manager has not created yet reads as 0 rather than as an error.
  scale_events() { # <up|down> [service-id]
    node_ssh 1 "curl -s --max-time 5 http://$health/metrics" 2>/dev/null \
      | awk -v dir="$1" -v id="${2:-}" '
          index($1, "swarmcracker_service_scale_events_total{") == 1 &&
            index($1, "direction=\"" dir "\"") > 0 &&
            (id == "" || index($1, "service=\"" id "\"") > 0) { total += $2 }
          END { printf "%.0f\n", total + 0 }'
  }

  # wait_replicas polls the manager until it reports <count> replicas, printing
  # every change. It returns 1 when the deadline passes.
  wait_replicas() { # <count> <budget-seconds> <label>
    local want="$1" budget="$2" label="$3" spent=0 cur last=""
    while [ "$spent" -lt "$budget" ]; do
      cur="$(spec_replicas "$svc")"
      if [ "$cur" != "$last" ]; then
        printf '  %3ss  %s: replicas=%s\n' "$spent" "$label" "${cur:-gone}"
        last="$cur"
      fi
      if [ "$cur" = "$want" ]; then return 0; fi
      sleep 5
      spent=$((spent + 5))
    done
    return 1
  }

  # wait_running polls until <count> replicas are RUNNING, so an assertion about
  # the count is not satisfied by replicas that were only requested.
  wait_running() { # <count> <budget-seconds>
    local want="$1" budget="$2" spent=0 r
    while [ "$spent" -lt "$budget" ]; do
      r="$(running_replicas "$svc")"
      if [ "${r:-0}" -ge "$want" ]; then return 0; fi
      sleep 5
      spent=$((spent + 5))
    done
    return 1
  }

  # task_hosts prints "<node> <task>" for every task of the service that has a
  # VM on a lab node, so `vm exec` can use that node's local vsock.
  task_hosts() {
    local j id
    for j in $(seq 1 "$n"); do
      for id in $(node_ssh 1 "swarmcracker service ps $svc -q 2>/dev/null" 2>/dev/null || true); do
        [ -n "$id" ] || continue
        if node_ssh "$j" "swarmcracker vm list 2>/dev/null" 2>/dev/null | awk '{print $1}' | grep -qx -- "$id"; then
          printf '%s %s\n' "$j" "$id"
        fi
      done
    done
  }

  # check reports one assertion. A failure is not fatal until the end, so a run
  # tells the operator everything that went wrong instead of the first thing.
  check() { # <0 when the check passed> <what the check proves>
    if [ "$1" = 0 ]; then
      printf '  PASS %s\n' "$2"
    else
      printf '  FAIL %s\n' "$2"
      rc=1
    fi
  }

  cleanup() {
    node_ssh 1 "swarmcracker service rm $svc --force >/dev/null 2>&1 || true" 2>/dev/null || true
  }

  # dump_autoscaler is the evidence a timed-out check needs: the replica view,
  # the counters and the manager's own decisions, all from the manager.
  dump_autoscaler() {
    printf '  service ps:\n'
    node_ssh 1 "swarmcracker service ps $svc 2>/dev/null" 2>/dev/null | sed 's/^/    /' || true
    printf '  scale events so far: up=%s down=%s\n' "$(scale_events up)" "$(scale_events down)"
    printf '  manager autoscaler log (last 20 lines):\n'
    node_ssh 1 "journalctl -u swarmcracker-manager -o cat --no-pager 2>/dev/null | grep -i autoscal | tail -20" 2>/dev/null \
      | sed 's/^/    /' || true
  }

  # Cleanup runs on every exit path, including a failing assertion: a leftover
  # hot service would keep burning a host's CPUs and skew the next run.
  trap cleanup EXIT

  info "autoscale e2e: $svc --autoscale min=$amin,max=$amax,metric=cpu,target=$atarget,cooldown-up=$cooldown_up,cooldown-down=$cooldown_down"

  info "cleanup: removing a leftover $svc from an earlier run"
  cleanup
  # Wait for its microVMs to be gone as well, not just its service entry: they
  # would keep the host's vCPUs busy and make this run's replicas look colder
  # than they are.
  for _ in $(seq 1 24); do
    cur="$(running_replicas "$svc")"
    if [ "${cur:-0}" = 0 ]; then break; fi
    sleep 5
  done

  info "scale-event counters before: the manager's $health/metrics on node 1"
  if ! node_ssh 1 "curl -sf --max-time 5 http://$health/metrics >/dev/null" 2>/dev/null; then
    die "no metrics from $health on node 1 (is the manager running, and is --health-addr $health?)"
  fi
  up_before="$(scale_events up)"
  down_before="$(scale_events down)"
  printf '  up=%s down=%s (summed over every service)\n' "$up_before" "$down_before"

  info "deploying $svc: $amin replica of $image, burning CPU until /tmp/stop exists"
  node_ssh 1 "swarmcracker service create --name $svc --image $image --replicas $amin \
    --autoscale min=$amin,max=$amax,metric=cpu,target=$atarget,cooldown-up=$cooldown_up,cooldown-down=$cooldown_down \
    --command=/bin/sh --args=-c --args='$burn'" >/dev/null
  svc_id="$(service_id "$svc")"
  [ -n "$svc_id" ] || die "the service was created but the manager does not list $svc"
  printf '  service ID: %s\n' "$svc_id"

  info "check 1: CPU load drives the count to max=$amax (deadline ${up_deadline}s)"
  if ! wait_replicas "$amax" "$up_deadline" "$svc"; then
    dump_autoscaler
    die "the autoscaler did not reach $amax replicas within ${up_deadline}s"
  fi
  ok=1
  if [ "$(spec_replicas "$svc")" = "$amax" ]; then ok=0; fi
  check "$ok" "replica count reached max=$amax on the autoscaler's own decision"
  ok=1
  if wait_running "$amax" 120; then ok=0; fi
  check "$ok" "all $amax replicas are RUNNING (the guests the scale-up added booted)"

  up_after="$(scale_events up)"
  up_for_service="$(scale_events up "$svc_id")"
  ok=1
  if [ "${up_after:-0}" -gt "${up_before:-0}" ]; then ok=0; fi
  check "$ok" "scale_events_total{direction=\"up\"} increased: ${up_before} -> ${up_after}"
  ok=1
  if [ "${up_for_service:-0}" -ge 1 ]; then ok=0; fi
  check "$ok" "this service's own 'up' series (label service=\"$svc_id\") is ${up_for_service}"

  info "check 2: stopping the load -- /tmp/stop in every replica"
  for i in 1 2 3; do
    touched=0
    while read -r host task; do
      if node_ssh "$host" "swarmcracker vm exec $task -- sh -c 'touch /tmp/stop && test -f /tmp/stop'" >/dev/null 2>&1; then
        touched=$((touched + 1))
      fi
    done < <(task_hosts)
    expected="$(running_replicas "$svc")"
    if [ "${touched:-0}" -gt 0 ] && [ "${touched:-0}" -ge "${expected:-0}" ]; then break; fi
    sleep 3
  done
  printf '  stopped %s of %s RUNNING replica(s)\n' "$touched" "${expected:-0}"
  if [ "${touched:-0}" = 0 ]; then
    dump_autoscaler
    die "could not write /tmp/stop in any replica, so the load never stops"
  fi

  info "check 3: the count returns to min=$amin (deadline ${down_deadline}s)"
  if ! wait_replicas "$amin" "$down_deadline" "$svc"; then
    dump_autoscaler
    die "the autoscaler did not scale back to $amin replicas within ${down_deadline}s"
  fi
  ok=1
  if [ "$(spec_replicas "$svc")" = "$amin" ]; then ok=0; fi
  check "$ok" "replica count returned to min=$amin once the load stopped"

  down_after="$(scale_events down)"
  down_for_service="$(scale_events down "$svc_id")"
  ok=1
  if [ "${down_after:-0}" -gt "${down_before:-0}" ]; then ok=0; fi
  check "$ok" "scale_events_total{direction=\"down\"} increased: ${down_before} -> ${down_after}"
  ok=1
  if [ "${down_for_service:-0}" -ge 1 ]; then ok=0; fi
  check "$ok" "this service's own 'down' series (label service=\"$svc_id\") is ${down_for_service}"

  info "check 4: a manual 'service scale 0' is a pause the autoscaler does not undo"
  node_ssh 1 "swarmcracker service scale $svc 0" >/dev/null
  if ! wait_replicas 0 120 "$svc"; then
    dump_autoscaler
    die "the service did not reach 0 replicas"
  fi
  up_paused="$(scale_events up)"
  down_paused="$(scale_events down)"
  printf '  at 0 replicas: up=%s down=%s; watching for %ss (more than two 30s passes)\n' \
    "$up_paused" "$down_paused" "$pause_window"
  stayed=0
  for i in $(seq 1 $((pause_window / 5))); do
    sleep 5
    cur="$(spec_replicas "$svc")"
    if [ "${cur:-gone}" != 0 ]; then
      printf '  the count left 0 after %ss: replicas=%s\n' "$((i * 5))" "${cur:-gone}"
      stayed=1
      break
    fi
  done
  ok=1
  if [ "$stayed" = 0 ]; then ok=0; fi
  check "$ok" "the count stayed 0 for ${pause_window}s"
  up_after="$(scale_events up)"
  down_after="$(scale_events down)"
  # `ok` is 1 only when a counter moved: a paused service must emit no events,
  # so "moved" is the failure here, unlike the checks above.
  ok=0
  if [ "${up_after:-0}" -gt "${up_paused:-0}" ] || [ "${down_after:-0}" -gt "${down_paused:-0}" ]; then ok=1; fi
  check "$ok" "neither counter moved while paused: up ${up_paused} -> ${up_after}, down ${down_paused} -> ${down_after}"

  info "the manager's decisions for $svc"
  node_ssh 1 "journalctl -u swarmcracker-manager -o cat --no-pager 2>/dev/null | grep -F -- 'service $svc_id' | tail -10" 2>/dev/null \
    | sed 's/^/  /' || true

  info "cleanup: removing $svc"
  cleanup
  trap - EXIT

  [ "$rc" = 0 ] || die "autoscale e2e failed"
  info "autoscale e2e passed: 1 -> $amax replicas under CPU load, back to $amin when the load stopped, and 0 stayed 0"
}

###############################################################################
# dispatch
###############################################################################
usage() { sed -n '2,/^set -euo pipefail$/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }

case "${1:-}" in
  build)      cmd_build ;;
  image)      cmd_image ;;
  create)     shift; cmd_create "${1:-}" ;;
  provision)  cmd_provision ;;
  cluster)    cmd_cluster ;;
  test)       cmd_test ;;
  ingress)    cmd_ingress ;;
  three-tier) cmd_three_tier ;;
  autoscale)  cmd_autoscale ;;
  status)     cmd_status ;;
  ssh)        shift; cmd_ssh "${1:-1}" ;;
  destroy|down) cmd_destroy ;;
  up)         shift; LAB_NODES="${1:-$LAB_NODES}"
              cmd_build; cmd_image; cmd_create "$LAB_NODES"; cmd_provision; cmd_cluster; cmd_status ;;
  ""|-h|--help|help) usage ;;
  *)          die "unknown command: $1 (try --help)" ;;
esac
