#!/bin/bash
# This script builds the mirrorer image (see the sibling "mirrorer/Dockerfile" file), tags it,
# and loads it into the current kind cluster (i.e. the cluster targeted by the current kubectl
# context), so that it is available to Pods running there without having to be pushed to a
# registry.
#
# Usage:
#     ./build-and-load-mirrorer-image.sh [image-tag]
#
# Example:
#     ./build-and-load-mirrorer-image.sh fleetdemo.azurecr.io/mirrorer:experimental
#
# Requirements:
#     - docker and the kind CLI (https://kind.sigs.k8s.io) must be installed and available in
#       PATH
#     - kubectl must be installed and available in PATH
#     - the current kubectl context must point to a kind cluster
set -euo pipefail

usage() {
    cat <<'EOF'
Usage:
    ./build-and-load-mirrorer-image.sh [image-tag]

Example:
    ./build-and-load-mirrorer-image.sh fleetdemo.azurecr.io/mirrorer:experimental

Requirements:
    - docker and the kind CLI must be installed and available in PATH
    - kubectl must be installed and available in PATH
    - the current kubectl context must point to a kind cluster
EOF
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
    usage
    exit 0
fi

if ! command -v docker >/dev/null 2>&1; then
    echo "Error: docker is not installed or not available in PATH" >&2
    exit 1
fi

if ! command -v kind >/dev/null 2>&1; then
    echo "Error: the kind CLI is not installed or not available in PATH" >&2
    exit 1
fi

if ! command -v kubectl >/dev/null 2>&1; then
    echo "Error: kubectl is not installed or not available in PATH" >&2
    exit 1
fi

IMAGE_TAG=${1:-fleetdemo.azurecr.io/mirrorer:experimental}

# Resolve the Dockerfile and the repository root (the Docker build context) relative to this
# script's own location (rather than the caller's working directory), so the script can be
# invoked from anywhere.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
DOCKERFILE="$SCRIPT_DIR/mirrorer/Dockerfile"

if [ ! -f "$DOCKERFILE" ]; then
    echo "Error: Dockerfile not found at $DOCKERFILE" >&2
    exit 1
fi

# kind always prefixes its kubeconfig context names with "kind-"; strip that prefix to recover
# the cluster name kind itself uses (e.g. for "kind get clusters"/"kind load docker-image").
CURRENT_CONTEXT="$(kubectl config current-context)"
case "$CURRENT_CONTEXT" in
    kind-*)
        KIND_CLUSTER_NAME="${CURRENT_CONTEXT#kind-}"
        ;;
    *)
        echo "Error: current kubectl context \"$CURRENT_CONTEXT\" does not look like a kind cluster context (expected a \"kind-<cluster-name>\" prefix)" >&2
        exit 1
        ;;
esac

if ! kind get clusters | grep -qx "$KIND_CLUSTER_NAME"; then
    echo "Error: no kind cluster named \"$KIND_CLUSTER_NAME\" was found" >&2
    exit 1
fi

echo "Building the mirrorer image as \"$IMAGE_TAG\"..."
docker build -f "$DOCKERFILE" -t "$IMAGE_TAG" "$REPO_ROOT"

echo "Loading \"$IMAGE_TAG\" into kind cluster \"$KIND_CLUSTER_NAME\"..."
kind load docker-image --name "$KIND_CLUSTER_NAME" "$IMAGE_TAG"

echo "Done. Image \"$IMAGE_TAG\" is now available in kind cluster \"$KIND_CLUSTER_NAME\"."
