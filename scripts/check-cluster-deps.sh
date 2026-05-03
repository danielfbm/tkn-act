#!/bin/sh
# check-cluster-deps.sh — verify docker, k3d, kubectl are on PATH.
#
# Used by `make doctor` (and reusable from anywhere) to give contributors
# a friendly preflight before they hit the binary. Versions are pinned to
# match .github/workflows/cluster-integration.yml so local runs match CI.
#
# Exit codes:
#   0 — all required tools on PATH
#   1 — at least one tool missing (install hints printed to stderr)
#
# Override the pinned versions via env if you intentionally drift:
#   K3D_VERSION=vX.Y.Z KUBECTL_VERSION=vX.Y.Z sh scripts/check-cluster-deps.sh

set -eu

K3D_VERSION="${K3D_VERSION:-v5.7.4}"
KUBECTL_VERSION="${KUBECTL_VERSION:-v1.31.0}"

missing=0

echo "tkn-act: cluster dependency check"
echo "  pinned: k3d=$K3D_VERSION  kubectl=$KUBECTL_VERSION  (see .github/workflows/cluster-integration.yml)"
echo

check_tool() {
  name=$1
  version_cmd=$2
  install_url=$3
  install_hint=$4

  if command -v "$name" >/dev/null 2>&1; then
    # Best-effort version line. Don't fail the script if --version errors.
    detected=$(eval "$version_cmd" 2>/dev/null | head -n 1 || true)
    printf '  [ok]   %-8s  %s\n' "$name" "${detected:-found on PATH}"
  else
    printf '  [MISS] %-8s  not on PATH\n' "$name" >&2
    printf '         install: %s\n' "$install_url" >&2
    if [ -n "$install_hint" ]; then
      printf '         hint:    %s\n' "$install_hint" >&2
    fi
    missing=$((missing + 1))
  fi
}

check_tool docker \
  "docker version --format '{{.Client.Version}}' || docker --version" \
  "https://docs.docker.com/get-docker/" \
  "ensure the daemon is running, e.g. 'systemctl --user start docker' or open Docker Desktop"

check_tool kubectl \
  "kubectl version --client=true -o yaml 2>/dev/null | grep -m1 gitVersion || kubectl version --client=true" \
  "https://kubernetes.io/docs/tasks/tools/#kubectl" \
  "curl -fsSL -o kubectl 'https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl' && chmod +x kubectl && sudo mv kubectl /usr/local/bin/kubectl"

check_tool k3d \
  "k3d version | head -n 1" \
  "https://k3d.io/#installation" \
  "curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | TAG=${K3D_VERSION} bash"

echo

if [ "$missing" -gt 0 ]; then
  echo "tkn-act: $missing required tool(s) missing — see hints above." >&2
  echo "         Cluster mode (--cluster, 'make cluster-up') is unavailable until installed." >&2
  exit 1
fi

echo "tkn-act: all cluster dependencies present."
exit 0
