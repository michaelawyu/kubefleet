#!/bin/bash
# This script bootstraps a vcluster for use in the KubeFleet multi-tenancy demo. It first
# creates a namespace on the host cluster (named after the vcluster itself), then creates a
# vcluster, configured via the sibling "vcluster.yaml" values file, in that namespace on the
# cluster targeted by the current kubectl context, then saves the vcluster's kubeconfig file to
# the current working directory as "<vcluster-name>.kubeconfig". It then applies the Kueue CRDs
# and RBAC resources (from the sibling "kueue/crds.yaml" and "kueue/rbac.yaml" files) to the
# vcluster, and mints a token-based kubeconfig for its "kueue-controller-manager" service
# account, saved to "<vcluster-name>.kueue.kubeconfig". Finally, it switches back to the host
# cluster and installs Kueue's ConfigMap/Secret/Service resources and Deployment (from the
# sibling "kueue/svc.configmap.secret.tmpl.host.yaml" and "kueue/deployment.tmpl.host.yaml"
# templates) into the vcluster's namespace there. If the namespace or a vcluster with the same
# name already exists, the script reports an error and exits without modifying anything.
#
# Usage:
#     ./setup-vcluster.sh [vcluster-name]
#
# Example:
#     ./setup-vcluster.sh tenant-a
#
# Requirements:
#     - kubectl and the vcluster CLI (https://www.vcluster.com) must be installed and available
#       in PATH
#     - the current kubectl context must point to a reachable Kubernetes cluster; this cluster
#       will act as the host cluster for the vcluster
set -euo pipefail

usage() {
    cat <<'EOF'
Usage:
    ./setup-vcluster.sh [vcluster-name]

Example:
    ./setup-vcluster.sh tenant-a

Requirements:
    - kubectl and the vcluster CLI must be installed and available in PATH
    - the current kubectl context must point to a reachable Kubernetes cluster; this cluster
      will act as the host cluster for the vcluster
EOF
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
    usage
    exit 0
fi

if ! command -v kubectl >/dev/null 2>&1; then
    echo "Error: kubectl is not installed or not available in PATH" >&2
    exit 1
fi

if ! command -v vcluster >/dev/null 2>&1; then
    echo "Error: the vcluster CLI is not installed or not available in PATH" >&2
    exit 1
fi

if ! command -v envsubst >/dev/null 2>&1; then
    echo "Error: envsubst is not installed or not available in PATH" >&2
    exit 1
fi

# Resolve the values file relative to this script's own location (rather than the caller's
# working directory), so the script can be invoked from anywhere.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VCLUSTER_VALUES_FILE="$SCRIPT_DIR/vcluster.yaml"
GATE_PODS_POLICY_FILE="$SCRIPT_DIR/policies/gate-pods.yaml"
KUEUE_CONFIG_FILE="$SCRIPT_DIR/kueue/config.yaml"

if [ ! -f "$VCLUSTER_VALUES_FILE" ]; then
    echo "Error: vcluster values file not found at $VCLUSTER_VALUES_FILE" >&2
    exit 1
fi

if [ ! -f "$GATE_PODS_POLICY_FILE" ]; then
    echo "Error: gate-pods policy file not found at $GATE_PODS_POLICY_FILE" >&2
    exit 1
fi

if [ ! -f "$KUEUE_CONFIG_FILE" ]; then
    echo "Error: Kueue config file not found at $KUEUE_CONFIG_FILE" >&2
    exit 1
fi

VCLUSTER_NAME=${1:-multitenancy-demo}
# Each vcluster is deployed into its own namespace; reuse the vcluster name so the two stay
# easy to correlate.
VCLUSTER_NAMESPACE=$VCLUSTER_NAME
KUBECONFIG_OUT="$(pwd)/$VCLUSTER_NAME.kubeconfig"

if kubectl get namespace "$VCLUSTER_NAMESPACE" >/dev/null 2>&1; then
    echo "Error: namespace \"$VCLUSTER_NAMESPACE\" already exists on the host cluster" >&2
    exit 1
fi

echo "Creating namespace \"$VCLUSTER_NAMESPACE\" on the host cluster..."
kubectl create namespace "$VCLUSTER_NAMESPACE"

if vcluster describe "$VCLUSTER_NAME" -n "$VCLUSTER_NAMESPACE" >/dev/null 2>&1; then
    echo "Error: vcluster \"$VCLUSTER_NAME\" already exists in namespace \"$VCLUSTER_NAMESPACE\"" >&2
    exit 1
fi

echo "Creating vcluster \"$VCLUSTER_NAME\" in namespace \"$VCLUSTER_NAMESPACE\"..."
vcluster create --upgrade "$VCLUSTER_NAME" -n "$VCLUSTER_NAMESPACE" --create-namespace --connect=false --values "$VCLUSTER_VALUES_FILE"

# The vcluster kubeconfig is published as a Secret ("vc-<name>", key "config") on the host
# cluster rather than retrieved via "vcluster connect", since the latter may block indefinitely
# waiting on a port-forward or background proxy in environments without Docker available.
echo "Waiting for the vcluster kubeconfig secret to become available..."
kubectl wait secret "vc-$VCLUSTER_NAME" -n "$VCLUSTER_NAMESPACE" --for=create --timeout=300s

echo "Saving the vcluster kubeconfig to $KUBECONFIG_OUT..."
kubectl get secret "vc-$VCLUSTER_NAME" -n "$VCLUSTER_NAMESPACE" -o jsonpath='{.data.config}' | base64 --decode > "$KUBECONFIG_OUT"

echo "Waiting for the API server Service of vcluster \"$VCLUSTER_NAME\" to be assigned a cluster IP..."
kubectl wait "service/$VCLUSTER_NAME" -n "$VCLUSTER_NAMESPACE" --for=jsonpath='{.spec.clusterIP}' --timeout=300s

echo "Starting port-forward.sh in the background for vcluster \"$VCLUSTER_NAME\"..."
"$SCRIPT_DIR/port-forward.sh" "$VCLUSTER_NAME" >/dev/null 2>&1 &
PORT_FORWARDER_PID=$!

echo "vcluster \"$VCLUSTER_NAME\" is ready. Its kubeconfig has been saved to $KUBECONFIG_OUT"

# Point kubectl at the vcluster (rather than running "vcluster connect", for the same reason
# noted above) using the kubeconfig just retrieved, so the following apply commands target the
# vcluster instead of the host cluster.
echo "Connecting to vcluster \"$VCLUSTER_NAME\"..."
# Remember the host cluster's kubeconfig so it can be restored later, once the vcluster-side
# setup is done.
HOST_KUBECONFIG="${KUBECONFIG:-}"
export KUBECONFIG="$KUBECONFIG_OUT"

echo "Applying the pod scheduler mutating admission policy to vcluster \"$VCLUSTER_NAME\"..."
kubectl apply -f "$GATE_PODS_POLICY_FILE"

echo "Setting up the Kueue installation..."
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/v0.19.5/manifests.yaml

echo "Waiting for the Kueue controller manager Deployment to become available..."
kubectl wait deploy/kueue-controller-manager -n kueue-system --for=condition=available --timeout=300s

echo "Applying the Kueue sample configuration to vcluster \"$VCLUSTER_NAME\"..."
kubectl apply -f "$KUEUE_CONFIG_FILE"


