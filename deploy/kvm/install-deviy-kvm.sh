#!/usr/bin/env bash
set -Eeuo pipefail

# Deviy KVM agent installer for Ubuntu 22.04 LTS.
# This script does not change the primary NIC, netplan, Docker bridges or the
# default route. It only adds the libvirt default NAT network (virbr0) and a
# dedicated nftables table managed by the agent.

MODE=""
TOKEN="${DEVIY_AGENT_TOKEN:-}"
LISTEN="${DEVIY_AGENT_LISTEN:-0.0.0.0:8443}"
NODE_ID="${DEVIY_NODE_ID:-kvm-01}"
NODE_NAME="${DEVIY_NODE_NAME:-KVM-01}"
REGION="${DEVIY_REGION:-HongKong}"
CONTROLLER_IP="${DEVIY_CONTROLLER_IP:-}"
KVM_STORAGE="${DEVIY_KVM_STORAGE:-/var/lib/libvirt/images/deviy}"
KVM_ISO_DIR="${DEVIY_KVM_ISO_DIR:-/var/lib/libvirt/images/iso}"
KVM_BACKUP_DIR="${DEVIY_KVM_BACKUP_DIR:-/var/lib/libvirt/images/backup}"
KVM_NETWORK="${DEVIY_KVM_NETWORK:-default}"
NAT_CIDR="${DEVIY_NAT_CIDR:-192.168.122.0/24}"
BINARY=""
PVE_URL="${DEVIY_PVE_URL:-https://127.0.0.1:8006}"
PVE_TOKEN="${DEVIY_PVE_TOKEN:-}"
PVE_NODE="${DEVIY_PVE_NODE:-pve}"
PVE_STORAGE="${DEVIY_PVE_STORAGE:-local-lvm}"
PVE_BRIDGE="${DEVIY_PVE_BRIDGE:-vmbr0}"
SKIP_PACKAGES=0

die() { echo "ERROR: $*" >&2; exit 1; }
info() { echo "[deviy] $*"; }

usage() {
  cat <<'USAGE'
Usage:
  sudo bash install-deviy-kvm.sh --mode bare --token <random-token> [options]

Modes:
  bare  Install KVM/libvirt and the Deviy KVM agent on Ubuntu 22.04.
  pve   Compatibility mode for an existing Proxmox VE host.
        This mode only installs the agent against the PVE API; it does not
        install the Ubuntu KVM/libvirt stack.

Options:
  --mode bare|pve
  --token TOKEN             Agent bearer token (required)
  --node-id ID              default: kvm-01
  --node-name NAME          default: KVM-01
  --region REGION           default: HongKong
  --listen ADDR:PORT        default: 0.0.0.0:8443
  --controller-ip CIDR      optional: restrict 8443 to this source
  --kvm-storage DIR         default: /var/lib/libvirt/images/deviy
  --kvm-iso-dir DIR         default: /var/lib/libvirt/images/iso
  --kvm-backup-dir DIR      default: /var/lib/libvirt/images/backup
  --binary PATH             default: ./deviy-agent-linux-amd64
  --pve-url URL             PVE API URL (pve mode)
  --pve-token TOKEN         PVE API token (pve mode)
  --pve-node NODE           PVE node name (pve mode)
  --pve-storage STORAGE     PVE storage ID (pve mode)
  --pve-bridge BRIDGE       PVE bridge (pve mode)
  --skip-packages           skip apt install (KVM/libvirt already installed)
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) MODE="${2:-}"; shift 2 ;;
    --token) TOKEN="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --node-name) NODE_NAME="${2:-}"; shift 2 ;;
    --region) REGION="${2:-}"; shift 2 ;;
    --listen) LISTEN="${2:-}"; shift 2 ;;
    --controller-ip) CONTROLLER_IP="${2:-}"; shift 2 ;;
    --kvm-storage) KVM_STORAGE="${2:-}"; shift 2 ;;
    --kvm-iso-dir) KVM_ISO_DIR="${2:-}"; shift 2 ;;
    --kvm-backup-dir) KVM_BACKUP_DIR="${2:-}"; shift 2 ;;
    --binary) BINARY="${2:-}"; shift 2 ;;
    --pve-url) PVE_URL="${2:-}"; shift 2 ;;
    --pve-token) PVE_TOKEN="${2:-}"; shift 2 ;;
    --pve-node) PVE_NODE="${2:-}"; shift 2 ;;
    --pve-storage) PVE_STORAGE="${2:-}"; shift 2 ;;
    --pve-bridge) PVE_BRIDGE="${2:-}"; shift 2 ;;
    --skip-packages) SKIP_PACKAGES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ "${EUID}" -eq 0 ]] || die "run as root"
[[ -n "${MODE}" ]] || {
  echo "Select installation mode:"
  echo "  1) Bare Ubuntu 22.04 + KVM/libvirt (recommended)"
  echo "  2) Existing Proxmox VE (compatibility; PVE agent)"
  read -r -p "Mode [1/2]: " choice
  [[ "${choice}" == "1" ]] && MODE=bare
  [[ "${choice}" == "2" ]] && MODE=pve
}
[[ "${MODE}" == "bare" || "${MODE}" == "pve" ]] || die "mode must be bare or pve"
[[ -n "${TOKEN}" ]] || die "--token is required"

if [[ -z "${BINARY}" ]]; then
  BINARY="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/deviy-agent-linux-amd64"
fi
[[ -f "${BINARY}" ]] || die "agent binary not found: ${BINARY}"

if [[ "${MODE}" == "pve" ]]; then
  command -v pveversion >/dev/null 2>&1 || die "pve mode requires Proxmox VE (pveversion not found)"
  [[ -n "${PVE_TOKEN}" ]] || die "--pve-token is required in pve mode"
else
  command -v apt-get >/dev/null 2>&1 || die "apt-get not found"
  . /etc/os-release
  [[ "${ID:-}" == "ubuntu" && "${VERSION_ID:-}" == "22.04" ]] || die "bare mode requires Ubuntu 22.04"
fi

info "Installing base packages"
export DEBIAN_FRONTEND=noninteractive
if [[ "${SKIP_PACKAGES}" == "1" ]]; then
  info "Skipping package installation (--skip-packages)"
else
  apt-get update -qq
  if [[ "${MODE}" == "bare" ]]; then
    if ! apt-get install -y -qq \
      qemu-kvm libvirt-daemon-system libvirt-clients virtinst qemu-utils \
      cloud-image-utils genisoimage nftables curl ca-certificates; then
      # Some pre-existing Ubuntu hosts have a half-configured grub package.
      # Do not hide a real failure: continue only when all KVM tools exist.
      if ! command -v virsh >/dev/null 2>&1 || ! command -v virt-install >/dev/null 2>&1 || ! command -v qemu-img >/dev/null 2>&1; then
        die "KVM package installation failed and required tools are missing"
      fi
      info "apt reported a non-fatal package warning; KVM tools are present, continuing"
    fi
  else
    apt-get install -y -qq nftables curl ca-certificates
  fi
fi

if [[ "${MODE}" == "bare" ]]; then
  info "Enabling libvirtd"
  systemctl enable --now libvirtd

  info "Ensuring libvirt default NAT network"
  if ! virsh net-info default >/dev/null 2>&1; then
    if [[ -f /usr/share/libvirt/networks/default.xml ]]; then
      virsh net-define /usr/share/libvirt/networks/default.xml
    else
      die "libvirt default network template not found"
    fi
  fi
  virsh net-start default >/dev/null 2>&1 || true
  virsh net-autostart default >/dev/null 2>&1 || true
  virsh net-info default | grep -q 'Active:.*yes' || die "libvirt default network is not active"

  ip link show virbr0 >/dev/null 2>&1 || die "virbr0 was not created"

  info "Creating KVM storage directories"
  install -d -m 0775 -o root -g libvirt "${KVM_STORAGE}" "${KVM_ISO_DIR}" "${KVM_BACKUP_DIR}" /var/lib/libvirt/images/templates

  info "Enabling IPv4 forwarding"
  cat >/etc/sysctl.d/99-deviy-kvm.conf <<'SYSCTL'
net.ipv4.ip_forward = 1
SYSCTL
  sysctl --system >/dev/null

  WAN_INTERFACE="$(ip -4 route show default | awk '{print $5; exit}')"
  [[ -n "${WAN_INTERFACE}" ]] || die "could not detect default WAN interface"
  info "WAN interface: ${WAN_INTERFACE}"
else
  WAN_INTERFACE="${DEVIY_WAN_INTERFACE:-$(ip -4 route show default | awk '{print $5; exit}')}"
  [[ -n "${WAN_INTERFACE}" ]] || die "could not detect WAN interface"
fi

info "Installing Deviy agent binary"
install -m 0755 "${BINARY}" /usr/local/bin/deviy-agent

AGENT_ARGS=(
  "--listen=${LISTEN}"
  "--node-id=${NODE_ID}"
  "--name=${NODE_NAME}"
  "--region=${REGION}"
  "--token=${TOKEN}"  "--network-backend=nftables"
  "--wan-interface=${WAN_INTERFACE}"
  "--nat-cidr=${NAT_CIDR}"
  "--allow-network-apply=true"
  "--metrics-backend=proc"
)

if [[ "${MODE}" == "bare" ]]; then
  AGENT_ARGS+=(
    "--mode=kvm"
    "--provider=kvm"
    "--kvm-uri=qemu:///system"
    "--kvm-storage=${KVM_STORAGE}"
    "--kvm-network=${KVM_NETWORK}"
    "--kvm-bridge=virbr0"
    "--kvm-iso-dir=${KVM_ISO_DIR}"
    "--kvm-backup-dir=${KVM_BACKUP_DIR}"
  )
else
  AGENT_ARGS+=(
    "--mode=pve"
    "--provider=pve"
    "--pve-url=${PVE_URL}"
    "--pve-token=${PVE_TOKEN}"
    "--pve-node=${PVE_NODE}"
    "--pve-storage=${PVE_STORAGE}"
    "--pve-bridge=${PVE_BRIDGE}"
  )
fi

info "Installing systemd service"
cat >/etc/systemd/system/deviy-agent.service <<UNIT
[Unit]
Description=Deviy KVM Agent
After=network-online.target libvirtd.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/deviy-agent ${AGENT_ARGS[*]}
Restart=always
RestartSec=5
User=root
Group=root

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable deviy-agent
systemctl restart deviy-agent
sleep 2
systemctl is-active deviy-agent >/dev/null || {
  journalctl -u deviy-agent -n 40 --no-pager || true
  die "deviy-agent failed to start"
}

info "Health check"
HEALTH="http://127.0.0.1:${LISTEN##*:}/health"
curl -fsS "${HEALTH}" || die "agent health check failed: ${HEALTH}"

if [[ -n "${CONTROLLER_IP}" ]]; then
  CONTROLLER_HOST="${CONTROLLER_IP%%/*}"
  if systemctl is-active --quiet ufw 2>/dev/null; then
    ufw allow from "${CONTROLLER_IP}" to any port "${LISTEN##*:}" proto tcp >/dev/null 2>&1 || true
    info "ufw rule added for controller source ${CONTROLLER_IP}"
  else
    info "ufw inactive; installing nftables management firewall"
    cat >/etc/deviy-mgmt.nft <<NFT
table inet deviy_mgmt {
  chain input {
    type filter hook input priority -10; policy accept;
    iif lo accept
    ip saddr ${CONTROLLER_HOST} accept
    tcp dport ${LISTEN##*:} drop
  }
}
NFT
    cat >/etc/systemd/system/deviy-mgmt-firewall.service <<'UNIT'
[Unit]
Description=Deviy KVM management firewall
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'nft delete table inet deviy_mgmt 2>/dev/null || true; nft -f /etc/deviy-mgmt.nft'
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT
    nft delete table inet deviy_mgmt 2>/dev/null || true
    nft -f /etc/deviy-mgmt.nft
    systemctl daemon-reload
    systemctl enable --now deviy-mgmt-firewall >/dev/null
    info "nftables rule added for controller source ${CONTROLLER_IP}"
  fi
fi

cat <<EOF

Deviy KVM agent installed.
  Mode:        ${MODE}
  Service:     deviy-agent
  API:         ${LISTEN}
  Health:      ${HEALTH}
  WAN:         ${WAN_INTERFACE}
  NAT CIDR:    ${NAT_CIDR}
  Storage:     ${KVM_STORAGE}
  Controller URL for host: http://<host-public-ip>:${LISTEN##*:}

Controller host fields:
  provider: kvm
  url: http://<host-public-ip>:${LISTEN##*:}
  token: same as --token
  public_bridge: virbr0
  private_bridge: virbr0

Security:
  Restrict TCP ${LISTEN##*:} to the Controller IP. Do not expose it to 0.0.0.0/0.
EOF
