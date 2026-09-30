#!/usr/bin/env bash
#
# Scratch notes for the examples/demo walkthrough -- not meant to be run end-to-end unattended.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Package the Kueue base manifests + overlays under test/oci/testdata/kueue as an OCI artifact and
# push it to the shared Azure Container Registry, fleetdemo.azurecr.io. This mirrors the "kueue"
# push in test/oci/setup.sh, but targets a real registry over HTTPS instead of the local,
# plain-HTTP registry that script spins up.

KUEUE_DIR="${REPO_ROOT}/test/oci/testdata/kueue"
REGISTRY_HOST="fleetdemo.azurecr.io"
KUEUE_ORAS_REF="${REGISTRY_HOST}/testdata/kueue:latest"
ORAS_ARTIFACT_TYPE="kubernetes/objects"

if ! command -v oras >/dev/null 2>&1; then
	echo "oras command not found; install ORAS CLI to package and push manifests" >&2
	exit 1
fi

# Authenticates the local Docker/ORAS credential store against fleetdemo.azurecr.io using the
# current Azure CLI session; requires "az login" and access to the fleetdemo registry beforehand.
az acr login --name fleetdemo

pushd "${KUEUE_DIR}" >/dev/null
oras push \
	--artifact-type "${ORAS_ARTIFACT_TYPE}" \
	"${KUEUE_ORAS_REF}" \
	"base" \
	"overlays"
popd >/dev/null

echo "Pushed OCI artifact: ${KUEUE_ORAS_REF}"

# Create an image pull secret, "acr-access", in the "work" namespace, so that Pods there can pull
# images from fleetdemo.azurecr.io.
#
# This is a test-only resource, so the credentials are inlined here in plain text rather than
# sourced from a secret manager or environment variable.
kubectl create secret docker-registry acr-access \
	--namespace work \
	--docker-server=fleetdemo.azurecr.io \
	--docker-username=fleetdemo \
	--docker-password='chndnLYxvcBGZ1JJCe6WvvHwqMlRQrHr7OAzcGQVNXzYNBTWnYvOJQQJ99CIACL93NaEqg7NAAACAZCRVrEn' \
	--dry-run=client -o yaml | kubectl apply -f -
