#!/usr/bin/env bash

set -euo pipefail

PORT="9000"
USERNAME="admin"
PASSWORD="testonly"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RAW_MANIFESTS_DIR="${SCRIPT_DIR}/testdata/raw"
REGISTRY_HOST="localhost:${PORT}"
ORAS_REF="${REGISTRY_HOST}/testdata/manifests:latest"
ORAS_ARTIFACT_TYPE="kubernetes/objects"

if ! command -v oras >/dev/null 2>&1; then
	echo "oras command not found; install ORAS CLI to package and push manifests" >&2
	exit 1
fi

# Unlike setup.sh, this refresh includes EXTRAS.md as part of the artifact.
ARTIFACT_PATHS=()
shopt -s nullglob dotglob
for path in "${RAW_MANIFESTS_DIR}"/*; do
	ARTIFACT_PATHS+=("${path##*/}")
done
shopt -u nullglob dotglob

if [ "${#ARTIFACT_PATHS[@]}" -eq 0 ]; then
	echo "no files or directories found under ${RAW_MANIFESTS_DIR}" >&2
	exit 1
fi

oras login "${REGISTRY_HOST}" \
	--username "${USERNAME}" \
	--password "${PASSWORD}" \
	--plain-http

pushd "${RAW_MANIFESTS_DIR}" >/dev/null
oras push --plain-http \
	--artifact-type "${ORAS_ARTIFACT_TYPE}" \
	"${ORAS_REF}" \
	"${ARTIFACT_PATHS[@]}"
popd >/dev/null

echo "Re-pushed OCI artifact (including EXTRAS.md): ${ORAS_REF}"
