#!/bin/bash
# This script bootstraps a vcluster for use in the KubeFleet multi-tenancy demo. It first
# creates a namespace on the host cluster (named after the vcluster itself), along with a
# "mirrorer" ServiceAccount in that namespace and a RoleBinding granting it the built-in
# "cluster-admin" ClusterRole scoped to that namespace (for the mirrorer Deployment; see the
# sibling "mirrorer/k8s.yaml" file). It then creates a vcluster, configured via the sibling
# "vcluster.yaml" values file, in that namespace on the cluster targeted by the current kubectl
# context, then saves the vcluster's kubeconfig file to the current working directory as
# "<vcluster-name>.kubeconfig". It then applies the Kueue CRDs and RBAC resources (from the
# sibling "kueue/crds.yaml" and "kueue/rbac.yaml" files) to the vcluster. Once Kueue is set up,
# it creates a "mirrorer" ServiceAccount in the vcluster (distinct from the one of the same
# name created earlier on the host cluster) with cluster-admin permissions there, mints a
# long-lived token for it, and builds a token-based kubeconfig for it, saved to
# "mirrorer.kubeconfig". Finally, it switches back to the host cluster and deploys the mirrorer
# itself (from the sibling "mirrorer/k8s.yaml" template, rendered with envsubst) into the
# vcluster's namespace there. If the namespace or a vcluster with the same name already exists,
# the script reports an error and exits without modifying anything.
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

if ! command -v openssl >/dev/null 2>&1; then
    echo "Error: openssl is not installed or not available in PATH" >&2
    exit 1
fi

# Resolve the values file relative to this script's own location (rather than the caller's
# working directory), so the script can be invoked from anywhere.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VCLUSTER_VALUES_FILE="$SCRIPT_DIR/vcluster.yaml"
GATE_PODS_POLICY_FILE="$SCRIPT_DIR/policies/gate-pods.yaml"
KUEUE_CONFIG_FILE="$SCRIPT_DIR/kueue/config.yaml"
MIRRORER_K8S_FILE="$SCRIPT_DIR/mirrorer/k8s.yaml"

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

if [ ! -f "$MIRRORER_K8S_FILE" ]; then
    echo "Error: mirrorer Kubernetes manifest template not found at $MIRRORER_K8S_FILE" >&2
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

echo "Creating ServiceAccount \"mirrorer\" in namespace \"$VCLUSTER_NAMESPACE\" on the host cluster..."
kubectl create serviceaccount mirrorer -n "$VCLUSTER_NAMESPACE"

# The built-in "admin" ClusterRole only covers a fixed allowlist of built-in API groups (plus
# whatever other ClusterRoles are labeled to aggregate into it); it does not cover KubeFleet's
# own CRDs (e.g. PlacementPolicy), since KubeFleet does not ship such an aggregation label.
# "cluster-admin" is used instead so that placementpolicymaker can manage those, while the
# RoleBinding below still keeps the grant scoped to this one namespace.
echo "Granting ServiceAccount \"mirrorer\" the \"cluster-admin\" role in namespace \"$VCLUSTER_NAMESPACE\" on the host cluster..."
kubectl create rolebinding mirrorer-admin -n "$VCLUSTER_NAMESPACE" --clusterrole=cluster-admin --serviceaccount="$VCLUSTER_NAMESPACE:mirrorer"

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
# openssl (rather than the "base64" command) is used here and below, since "base64" resolves to
# a number of mutually-incompatible tools across platforms (GNU coreutils, BSD, and others);
# openssl's own "base64" subcommand behaves identically everywhere it is available.
kubectl get secret "vc-$VCLUSTER_NAME" -n "$VCLUSTER_NAMESPACE" -o jsonpath='{.data.config}' | openssl base64 -d -A > "$KUBECONFIG_OUT"

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

MIRRORER_KUBECONFIG_OUT="$(pwd)/mirrorer.kubeconfig"

echo "Creating ServiceAccount \"mirrorer\" in the vcluster..."
kubectl create serviceaccount mirrorer -n default

echo "Granting ServiceAccount \"mirrorer\" cluster-admin permissions in the vcluster..."
kubectl create clusterrolebinding mirrorer-cluster-admin --clusterrole=cluster-admin --serviceaccount=default:mirrorer

# Tokens minted via "kubectl create token" are short-lived (1 hour) by default; a much longer
# duration is used here since this token is meant to back a long-running Deployment (the
# mirrorer), not a one-off client.
echo "Requesting a service account token for ServiceAccount \"mirrorer\"..."
MIRRORER_TOKEN=$(kubectl create token mirrorer -n default --duration=8760h)

echo "Building a token-based kubeconfig for ServiceAccount \"mirrorer\" at $MIRRORER_KUBECONFIG_OUT..."
# Start from the vcluster's own (cert-based) kubeconfig, so its CA settings are reused as-is,
# then swap the current context's user over to the mirrorer ServiceAccount token.
cp "$KUBECONFIG_OUT" "$MIRRORER_KUBECONFIG_OUT"
MIRRORER_CONTEXT_NAME=$(kubectl config --kubeconfig="$MIRRORER_KUBECONFIG_OUT" current-context)
MIRRORER_CLUSTER_NAME=$(kubectl config --kubeconfig="$MIRRORER_KUBECONFIG_OUT" view -o jsonpath="{.contexts[?(@.name==\"$MIRRORER_CONTEXT_NAME\")].context.cluster}")

# The vcluster's own kubeconfig, as retrieved from its Secret, points its server at
# "https://localhost:<port>" (and, by the time this runs, port-forward.sh may have already
# rewritten that to whatever local port it is forwarding); either way, that address is reachable
# only from a client going through a port-forward, not from a Pod running inside the host
# cluster (where the mirrorer actually runs). Point it at the vcluster API server's in-cluster
# Service DNS name instead (e.g. "team-red.team-red" for a vcluster named "team-red"), which is
# reachable directly, on the default HTTPS port, with no port-forwarding needed.
kubectl config --kubeconfig="$MIRRORER_KUBECONFIG_OUT" set-cluster "$MIRRORER_CLUSTER_NAME" --server="https://$VCLUSTER_NAME.$VCLUSTER_NAMESPACE"
kubectl config --kubeconfig="$MIRRORER_KUBECONFIG_OUT" set-credentials mirrorer --token="$MIRRORER_TOKEN"
kubectl config --kubeconfig="$MIRRORER_KUBECONFIG_OUT" set-context "$MIRRORER_CONTEXT_NAME" --user=mirrorer

echo "ServiceAccount \"mirrorer\"'s kubeconfig has been saved to $MIRRORER_KUBECONFIG_OUT"

# Restore whatever KUBECONFIG was in effect before this script switched to the vcluster (or
# unset it entirely if none was set), so the mirrorer Deployment below is applied to the host
# cluster, not the vcluster.
echo "Leaving vcluster \"$VCLUSTER_NAME\"..."
if [ -n "$HOST_KUBECONFIG" ]; then
    export KUBECONFIG="$HOST_KUBECONFIG"
else
    unset KUBECONFIG
fi

echo "Deploying the mirrorer to namespace \"$VCLUSTER_NAMESPACE\" on the host cluster..."
export MIRRORER_IMAGE="${MIRRORER_IMAGE:-fleetdemo.azurecr.io/mirrorer:experimental}"
export MIRRORER_SERVICE_ACCOUNT=mirrorer
export VCLUSTER_NAME
# The mirrorer talks to the vcluster as the "mirrorer" ServiceAccount created above (not as the
# vcluster's own admin user), so it is mirrorer.kubeconfig, rather than $KUBECONFIG_OUT, that is
# embedded here.
export VCLUSTER_KUBECONFIG="$(openssl base64 -A < "$MIRRORER_KUBECONFIG_OUT")"
envsubst < "$MIRRORER_K8S_FILE" | kubectl apply -n "$VCLUSTER_NAMESPACE" -f -

echo "The mirrorer has been deployed to namespace \"$VCLUSTER_NAMESPACE\" on the host cluster."


