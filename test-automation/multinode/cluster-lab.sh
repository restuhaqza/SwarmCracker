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
# half minutes.
#
# Every budget is the mechanism's own numbers plus margin, and the worst case
# adds up to 620s: 150 for the count to reach max, 45 for the guests to boot, 45
# for the up event to show, 150 back to min, 45 + 45 for the down event and its
# guests, 45 to reach 0, 95 paused. That ceiling is not the expected runtime: the
# counter polls normally answer on the first read (the event is emitted as the
# patch is applied, before the count reaches its target), so a passing run is
# about 6 minutes, and a run that sits on both counter deadlines is failing
# anyway. On a slow host raise LAB_AUTOSCALE_EVENT_DEADLINE before the count
# deadlines: the counter is the noisy measurement, the count is not.
cmd_autoscale() {
  need_root
  [ -n "$(node_ip 1 || true)" ] || die "no nodes found; run: $0 create"

  local n="$LAB_NODES"
  local svc="${LAB_AUTOSCALE_PREFIX:-scas}"
  local image="${LAB_AUTOSCALE_IMAGE:-$LAB_DB_IMAGE}"
  local health="${LAB_AUTOSCALE_HEALTH_ADDR:-127.0.0.1:8080}"
  local amin=1 amax=3 atarget=40
  local cooldown_up=30s cooldown_down=60s
  # The up budget covers two autoscaler passes (a replica's first sample is only
  # a baseline) plus the third guest's boot; the down budget covers two cold
  # samples, the 60s down cooldown and two more cold samples. Both are the
  # mechanism's own numbers plus ~40s, not round numbers.
  local up_deadline="${LAB_AUTOSCALE_UP_DEADLINE:-150}"
  local down_deadline="${LAB_AUTOSCALE_DOWN_DEADLINE:-150}"
  local run_deadline=45 zero_deadline=45 pause_window=95
  # The counter is never trusted from one scrape. The autoscaler emits its event
  # asynchronously right after the patch, and a node in the middle of image prep
  # can answer /metrics with a body that stops short of the scale-event family
  # (four live runs read exactly that). Both are races, and neither is a value:
  # the assertions poll, and an unreadable read is retried, never read as 0.
  local event_deadline="${LAB_AUTOSCALE_EVENT_DEADLINE:-45}"
  local event_interval=5 read_deadline="${LAB_AUTOSCALE_READ_DEADLINE:-20}"
  local rc=0
  local svc_id="" up_before="" down_before="" down_before_stop=""
  local up_paused="" down_paused="" up_paused_after="" down_paused_after=""
  local scrape_value="" scrape_error="" scrape_evidence="" read_events_value=""
  local poll_all="" poll_service="" poll_error="" poll_reads=0
  local paused_reads=0 moved=0 paused_error="" up_now="" down_now=""
  local stop_done="" stop_touched=0 stop_expected=0 stop_missed=""
  local ok host task i cur stayed leftover scale_ok

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

  # scrape_events reads swarmcracker_service_scale_events_total off the
  # manager's /metrics and sums it for one direction, optionally narrowed to one
  # service ID. It sets scrape_value on success, or returns 1 with the reason in
  # scrape_error and the wire evidence in scrape_evidence.
  #
  # The reading is strict on purpose. This function used to be
  # `curl -s ... | awk ... total + 0`, which reported "0 events" for a manager
  # that was not answering at all -- and since the paused check is a *negative*
  # assertion ("nothing moved"), a dead endpoint passed it, because 0 -> 0 looks
  # exactly like "no events". So an untrustworthy reading is never a value: the
  # caller retries it (read_events, wait_scale_event), and only a deadline turns
  # it into a failure.
  #
  # "Untrustworthy" is one of four things, each named in scrape_error:
  #
  #   - curl failed (a timeout, a 429 from the daemon's 10 rps limit, a 5xx);
  #   - the body is empty;
  #   - the body carries no swarmcracker_ lines at all (a proxy page, the wrong
  #     port, another daemon's /metrics);
  #   - the body stops short of swarmcracker_vxlan_peers, the family a complete
  #     exposition publishes last (families are sorted by name, and of this
  #     package's names vxlan_peers sorts after vms_*, vxlan_expected_peers and
  #     everything else). This is the failure the live lab kept producing: a
  #     200KB exposition, curl exit 0, 83 swarmcracker_ lines, and no
  #     scale-event family -- a complete-looking body cut off before it.
  #
  # Two things are a legitimate 0 and keep the `+ 0`:
  #
  #   - the family is there but has no series for this service: a service that
  #     never scaled has no series yet (a CounterVec only renders the label
  #     combinations it has seen);
  #   - passed `baseline`, the family itself may be missing: a CounterVec with
  #     no children renders nothing at all, so a manager that has never recorded
  #     a scale event publishes no such family (measured: an empty
  #     prometheus.NewCounterVec produces an empty exposition, one Inc()
  #     produces the family). Without that the pre-create baseline could not be
  #     read on a fresh lab. Only the baseline is read that way; every later read
  #     is strict, so a manager that goes away between the baseline and an
  #     assertion still stops the run instead of passing it as 0 -> 0.
  scrape_events() { # <up|down> [service-id] [baseline]
    local dir="$1" id="${2:-}" allow_missing_family="${3:-}" body="" crc=0
    local bytes=0 families=0 complete=no first=""
    scrape_value="" scrape_error="" scrape_evidence=""
    # --max-time 20, not 5: a 200KB exposition on a node doing disk copies can
    # legitimately take longer than 5s, and a client timeout looks exactly like
    # a broken endpoint from here.
    body="$(node_ssh 1 "curl -sf --max-time 20 http://$health/metrics" 2>/dev/null)" || crc=$?
    if [ -n "$body" ]; then
      bytes="$(printf '%s' "$body" | wc -c | tr -d '[:space:]')"
      families="$(printf '%s\n' "$body" | grep -c '^swarmcracker_' || true)"
      if printf '%s\n' "$body" | grep -q '^swarmcracker_vxlan_peers'; then complete=yes; fi
      first="$(printf '%s\n' "$body" | head -1 | cut -c1-120)"
    fi
    # Everything a re-run needs to classify this failure without a lab: the
    # client's exit status, how much arrived, how much of it was ours, and
    # whether the exposition reached its end.
    scrape_evidence="curl exit $crc, ${bytes} bytes, ${families} swarmcracker_ lines, ends with swarmcracker_vxlan_peers: $complete"
    if [ "$crc" -ne 0 ]; then
      scrape_error="curl could not read http://$health/metrics on node 1 ($scrape_evidence; is the manager running, and is --health-addr $health?)"
      return 1
    fi
    if [ -z "$body" ]; then
      scrape_error="http://$health/metrics on node 1 answered with an empty body ($scrape_evidence)"
      return 1
    fi
    if [ "$families" -eq 0 ]; then
      scrape_error="http://$health/metrics on node 1 is not the manager's metrics exposition ($scrape_evidence; first 120 characters: $first)"
      return 1
    fi
    if [ "$complete" != yes ]; then
      scrape_error="http://$health/metrics on node 1 returned a truncated exposition ($scrape_evidence)"
      return 1
    fi
    if ! printf '%s\n' "$body" | grep -q '^swarmcracker_service_scale_events_total{'; then
      if [ "$allow_missing_family" = baseline ]; then
        scrape_value=0
        return 0
      fi
      scrape_error="http://$health/metrics on node 1 has no swarmcracker_service_scale_events_total family ($scrape_evidence)"
      return 1
    fi
    scrape_value="$(printf '%s\n' "$body" | awk -v dir="$dir" -v id="$id" '
        index($1, "swarmcracker_service_scale_events_total{") == 1 &&
          index($1, "direction=\"" dir "\"") > 0 &&
          (id == "" || index($1, "service=\"" id "\"") > 0) { total += $2 }
        END { printf "%.0f\n", total + 0 }')"
    if [ -z "$scrape_value" ]; then
      scrape_error="the $dir counter could not be summed out of http://$health/metrics on node 1 ($scrape_evidence)"
      return 1
    fi
  }

  # read_events is scrape_events with a bounded retry and the hard stop: a
  # counter that could not be read is unknown, never 0, and a bare assignment
  # under `set -e` would abort the run with no message at all.
  #
  # The retry is the point. A scrape that comes back truncated or foreign while a
  # node is doing image prep is a temporary condition, not a counter value, so it
  # is retried; only the deadline makes it fatal, and the message says how many
  # attempts were made and what the last one saw.
  read_events() { # <up|down> [service-id] [baseline]; sets read_events_value
    local spent=0 attempts=0 why=""
    while :; do
      attempts=$((attempts + 1))
      if scrape_events "$@"; then
        read_events_value="$scrape_value"
        return 0
      fi
      why="$scrape_error"
      if [ "$spent" -ge "$read_deadline" ]; then
        die "cannot read the $1 scale-event counter after $attempts attempts over ${spent}s: $why"
      fi
      printf '  the %s counter is not readable yet (attempt %s); retrying in 2s: %s\n' "$1" "$attempts" "$why"
      sleep 2
      spent=$((spent + 2))
    done
  }

  # wait_scale_event polls one direction of the counter until both readings have
  # moved: the sum over every service past <want-all> (the baseline this run read
  # before the phase) and this service's own series to <want-service> (1). One
  # scrape is a race, not an observation -- the event is emitted asynchronously
  # right after the patch -- so this waits for the event instead of sampling it
  # once and calling a slow manager a missing one.
  #
  # An unreadable attempt is retried, never evaluated: it is neither a move nor a
  # non-move. Sets poll_all/poll_service to the last readable pair, poll_reads to
  # the number of readable reads, and on a deadline poll_error to the last
  # diagnosis (which the caller prints before dying) rather than a bare value.
  wait_scale_event() { # <up|down> <service-id> <want-all> <want-service>
    local dir="$1" id="$2" want_all="$3" want_service="$4"
    local spent=0 why="" all="" svc=""
    poll_all="" poll_service="" poll_error="" poll_reads=0
    while :; do
      all="" svc=""
      if scrape_events "$dir"; then
        all="$scrape_value"
      else
        why="$scrape_error"
      fi
      if [ -n "$all" ]; then
        if scrape_events "$dir" "$id"; then
          svc="$scrape_value"
        else
          why="$scrape_error"
          all=""
        fi
      fi
      if [ -n "$all" ]; then
        poll_reads=$((poll_reads + 1))
        poll_all="$all" poll_service="$svc"
        printf '  %3ss  %s: sum=%s (want %s) this service=%s (want %s)\n' \
          "$spent" "$dir" "$all" "$want_all" "$svc" "$want_service"
        if [ "$all" -ge "$want_all" ] && [ "$svc" -ge "$want_service" ]; then
          poll_error=""
          return 0
        fi
        why="read sum=$all and this service=$svc; wanted >$((want_all - 1)) and >=$want_service"
      else
        printf '  %3ss  %s: unreadable, retrying: %s\n' "$spent" "$dir" "$why"
      fi
      if [ "$spent" -ge "$event_deadline" ]; then
        poll_error="$why"
        return 1
      fi
      sleep "$event_interval"
      spent=$((spent + event_interval))
    done
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

  # try_stop writes /tmp/stop in every replica it can find on a lab node and
  # appends the task IDs it succeeded in to stop_done.
  try_stop() {
    local host task
    while read -r host task; do
      [ -n "$task" ] || continue
      case " $stop_done " in *" $task "*) continue ;; esac
      if node_ssh "$host" "swarmcracker vm exec $task -- sh -c 'touch /tmp/stop && test -f /tmp/stop'" >/dev/null </dev/null 2>&1; then
        stop_done="$stop_done $task"
      fi
    done < <(task_hosts)
  }

  # stop_replicas writes /tmp/stop in every replica of the service and sets:
  #   stop_touched   replicas the write succeeded in
  #   stop_expected  replicas the manager reports RUNNING
  #   stop_missed    the RUNNING task IDs that never got the file
  #
  # Every replica must get the file, not just one: the autoscaler averages over
  # the replicas that report samples, so a single replica still burning keeps the
  # average above the scale-in band while the others are cold -- the scale-in
  # check would then fail (or, worse, "pass" on another service's events) with
  # one guest left hot. It re-enumerates once before giving up, because a replica
  # can be replaced between the enumeration and the write: `vm exec` against the
  # dead task fails, and the replacement is a different task that needs its own
  # file.
  stop_replicas() {
    local host task missed
    stop_done="" stop_touched=0 stop_expected=0 stop_missed=""
    try_stop
    stop_expected="$(running_replicas "$svc")"
    stop_touched="$(printf '%s\n' "$stop_done" | wc -w | tr -d '[:space:]')"
    if [ "$stop_touched" -lt "${stop_expected:-0}" ]; then
      try_stop
      stop_expected="$(running_replicas "$svc")"
      stop_touched="$(printf '%s\n' "$stop_done" | wc -w | tr -d '[:space:]')"
    fi
    missed=""
    while read -r host task; do
      [ -n "$task" ] || continue
      case " $stop_done " in *" $task "*) ;; *) missed="$missed $task" ;; esac
    done < <(task_hosts)
    stop_missed="${missed# }"
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
  # the counters and the manager's own decisions, all from the manager. The
  # counters are read softly here -- the run is already failing, and a second
  # hard stop would replace the dump with a die.
  dump_autoscaler() {
    local up='?' down='?'
    printf '  service ps:\n'
    node_ssh 1 "swarmcracker service ps $svc 2>/dev/null" 2>/dev/null | sed 's/^/    /' || true
    if scrape_events up; then up="$scrape_value"; fi
    if scrape_events down; then down="$scrape_value"; fi
    printf '  scale events so far: up=%s down=%s\n' "$up" "$down"
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
  # than they are. This only warns when it expires -- the run is still correct,
  # its first CPU samples are just skewed by whatever is still burning.
  leftover=1
  for i in $(seq 1 24); do
    cur="$(running_replicas "$svc")"
    if [ "${cur:-0}" = 0 ]; then leftover=0; break; fi
    if [ "$i" = 1 ]; then
      info "waiting up to 120s for the old $svc microVMs to exit, so their load does not skew this run's CPU samples"
    fi
    sleep 5
  done
  if [ "$leftover" != 0 ]; then
    warn "old $svc tasks are still RUNNING after 120s; the first CPU samples of this run may read colder than they are"
  fi

  info "scale-event counters before: the manager's $health/metrics on node 1"
  # The baseline is the only read that tolerates a missing metric family: before
  # the first scale event ever, the manager publishes none (see scrape_events),
  # and the sum over every service is then genuinely 0.
  read_events up "" baseline
  up_before="$read_events_value"
  read_events down "" baseline
  down_before="$read_events_value"
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
  if wait_running "$amax" "$run_deadline"; then ok=0; fi
  check "$ok" "all $amax replicas are RUNNING (the guests the scale-up added booted)"

  info "check 2: the up counter moved (polling up to ${event_deadline}s, ${event_interval}s apart)"
  # Polled, not sampled once: the event is emitted right after the patch, and a
  # single scrape a second later is a race against the manager's own bookkeeping
  # -- exactly the race the live lab lost four times.
  if ! wait_scale_event up "$svc_id" "$((up_before + 1))" 1; then
    check 1 "scale_events_total{direction=\"up\"} reached $((up_before + 1)) within ${event_deadline}s"
    dump_autoscaler
    die "scale_events_total{direction=\"up\"} did not move past the ${up_before} baseline within ${event_deadline}s after $poll_reads readable read(s): $poll_error"
  fi
  check 0 "scale_events_total{direction=\"up\"} increased: ${up_before} -> ${poll_all}"
  check 0 "this service's own 'up' series (label service=\"$svc_id\") is ${poll_service}"

  info "check 3: stopping the load -- /tmp/stop in every replica"
  # The down baseline is taken here, immediately before the writes, so the event
  # that check 5 measures can only be this scenario's: anything that scaled
  # between the create and now is not counted as ours.
  read_events down
  down_before_stop="$read_events_value"
  stop_replicas
  printf '  stopped %s of %s RUNNING replica(s)\n' "$stop_touched" "${stop_expected:-0}"
  ok=1
  if [ -z "$stop_missed" ] && [ "${stop_touched:-0}" -ge "${stop_expected:-0}" ]; then ok=0; fi
  check "$ok" "every replica got /tmp/stop (${stop_touched} of ${stop_expected:-0}; missed: ${stop_missed:-none})"
  if [ "$ok" != 0 ]; then
    dump_autoscaler
    if [ "${stop_touched:-0}" = 0 ]; then
      die "could not write /tmp/stop in any replica, so the load never stops"
    fi
    warn "a replica without /tmp/stop keeps burning, so the scale-in checks below may measure the wrong thing"
  fi

  info "check 4: the count returns to min=$amin (deadline ${down_deadline}s)"
  if ! wait_replicas "$amin" "$down_deadline" "$svc"; then
    dump_autoscaler
    die "the autoscaler did not scale back to $amin replicas within ${down_deadline}s"
  fi
  ok=1
  if [ "$(spec_replicas "$svc")" = "$amin" ]; then ok=0; fi
  check "$ok" "replica count returned to min=$amin once the load stopped"

  info "check 5: the down counter moved (polling up to ${event_deadline}s, ${event_interval}s apart)"
  # Against down_before_stop, the value read immediately before the /tmp/stop
  # writes, so the event can only be this scenario's -- and polled for the same
  # reason the up counter is.
  if ! wait_scale_event down "$svc_id" "$((down_before_stop + 1))" 1; then
    check 1 "scale_events_total{direction=\"down\"} reached $((down_before_stop + 1)) within ${event_deadline}s"
    dump_autoscaler
    die "scale_events_total{direction=\"down\"} did not move past the ${down_before_stop} baseline within ${event_deadline}s after $poll_reads readable read(s): $poll_error"
  fi
  check 0 "scale_events_total{direction=\"down\"} increased since the load stopped: ${down_before_stop} -> ${poll_all}"
  check 0 "this service's own 'down' series (label service=\"$svc_id\") is ${poll_service}"

  info "check 6: a manual 'service scale 0' is a pause the autoscaler does not undo"
  scale_ok=1
  if node_ssh 1 "swarmcracker service scale $svc 0" >/dev/null 2>&1; then scale_ok=0; fi
  check "$scale_ok" "'service scale $svc 0' was accepted by the manager"
  if [ "$scale_ok" != 0 ]; then
    dump_autoscaler
    die "the manager refused 'service scale $svc 0', so the pause cannot be tested"
  fi
  if ! wait_replicas 0 "$zero_deadline" "$svc"; then
    dump_autoscaler
    die "the service did not reach 0 replicas"
  fi
  # The paused window is read from this service's own series, with the same ID
  # used by the checks above: summing over every service would make another
  # autoscaled service in the lab fail the paused assertion spuriously (I4).
  # It is also the one *negative* assertion: it has to prove the counter did not
  # move, so a read it cannot trust is not evidence of stillness (R2/R4). Every
  # in-window read that answers with a complete exposition counts; a window with
  # fewer than two of them is a FAIL, because "unknown" is not "did not move".
  read_events up "$svc_id"
  up_paused="$read_events_value"
  read_events down "$svc_id"
  down_paused="$read_events_value"
  up_paused_after="$up_paused" down_paused_after="$down_paused"
  printf '  at 0 replicas: up=%s down=%s (service=%s); watching for %ss (more than two 30s passes)\n' \
    "$up_paused" "$down_paused" "$svc_id" "$pause_window"
  stayed=0 moved=0 paused_reads=0 paused_error=""
  for i in $(seq 1 $((pause_window / 5))); do
    sleep 5
    cur="$(spec_replicas "$svc")"
    if [ "${cur:-gone}" != 0 ]; then
      printf '  the count left 0 after %ss: replicas=%s\n' "$((i * 5))" "${cur:-gone}"
      stayed=1
      break
    fi
    if ! scrape_events up "$svc_id"; then
      paused_error="$scrape_error"
      continue
    fi
    up_now="$scrape_value"
    if ! scrape_events down "$svc_id"; then
      paused_error="$scrape_error"
      continue
    fi
    down_now="$scrape_value"
    paused_reads=$((paused_reads + 1))
    up_paused_after="$up_now" down_paused_after="$down_now"
    if [ "$up_now" -gt "$up_paused" ] || [ "$down_now" -gt "$down_paused" ]; then
      printf '  a scale event appeared after %ss while paused: up %s -> %s, down %s -> %s\n' \
        "$((i * 5))" "$up_paused" "$up_now" "$down_paused" "$down_now"
      # Recorded, not acted on: the window keeps watching, so the count check
      # still sees a service that a scale event put back above 0.
      moved=1
    fi
  done
  ok=1
  if [ "$stayed" = 0 ]; then ok=0; fi
  check "$ok" "the count stayed 0 for ${pause_window}s"
  if [ "$paused_reads" -lt 2 ]; then
    printf '  only %s readable counter read(s) in the %ss pause window, so "did not move" is unproven\n' \
      "$paused_reads" "$pause_window"
    if [ -n "$paused_error" ]; then
      printf '  the last unreadable read said: %s\n' "$paused_error"
    fi
  fi
  # This check passes only on two or more readable reads that never moved: a
  # missing read is not a quiet one.
  ok=1
  if [ "$paused_reads" -ge 2 ] && [ "$moved" = 0 ]; then ok=0; fi
  check "$ok" "this service emitted no scale event while paused: up ${up_paused} -> ${up_paused_after}, down ${down_paused} -> ${down_paused_after} (${paused_reads} readable reads in ${pause_window}s)"

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
