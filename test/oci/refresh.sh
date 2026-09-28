#!/usr/bin/env bash

set -euo pipefail

PORT="9000"
USERNAME="admin"
PASSWORD="testonly"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RAW_MANIFESTS_DIR="${SCRIPT_DIR}/testdata/raw"
KUEUE_DIR="${SCRIPT_DIR}/testdata/kueue"
REGISTRY_HOST="localhost:${PORT}"
ORAS_REF="${REGISTRY_HOST}/testdata/manifests:latest"
KUEUE_ORAS_REF="${REGISTRY_HOST}/testdata/kueue:latest"
ORAS_ARTIFACT_TYPE="kubernetes/objects"
CLUSTER_QUEUE_FILE="${KUEUE_DIR}/base/cluster-queue.yaml"
BOOST_KUSTOMIZATION_FILE="${KUEUE_DIR}/overlays/boost/kustomization.yaml"

# Portable in-place sed: macOS requires an explicit backup extension with -i,
# while GNU sed (Linux) treats an empty string as no backup.
sed_in_place() {
	if sed --version 2>/dev/null | grep -q 'GNU'; then
		sed -i "$@"
	else
		sed -i '' "$@"
	fi
}

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

# Back up the two Kueue fixtures below so the doubled quotas can be reset on the local FS once
# the refreshed push completes; these files are untracked, so a git-based reset isn't an option.
cp "${CLUSTER_QUEUE_FILE}" "${CLUSTER_QUEUE_FILE}.orig"
cp "${BOOST_KUSTOMIZATION_FILE}" "${BOOST_KUSTOMIZATION_FILE}.orig"
restore_kueue_fixtures() {
	mv "${CLUSTER_QUEUE_FILE}.orig" "${CLUSTER_QUEUE_FILE}"
	mv "${BOOST_KUSTOMIZATION_FILE}.orig" "${BOOST_KUSTOMIZATION_FILE}"
}
trap restore_kueue_fixtures EXIT

# Double the base ClusterQueue's quotas (10/10Gi -> 20/20Gi) ...
sed_in_place \
	-e 's/nominalQuota: 10$/nominalQuota: 20/' \
	-e 's/nominalQuota: 10Gi$/nominalQuota: 20Gi/' \
	"${CLUSTER_QUEUE_FILE}"

# ... and the boost overlay's JSON 6902 patch values to match (20/20Gi -> 40/40Gi).
sed_in_place \
	-e 's/value: 20$/value: 40/' \
	-e 's/value: 20Gi$/value: 40Gi/' \
	"${BOOST_KUSTOMIZATION_FILE}"

pushd "${KUEUE_DIR}" >/dev/null
oras push --plain-http \
	--artifact-type "${ORAS_ARTIFACT_TYPE}" \
	"${KUEUE_ORAS_REF}" \
	"base" \
	"overlays"
popd >/dev/null

echo "Re-pushed OCI artifact (with doubled ClusterQueue quotas): ${KUEUE_ORAS_REF}"

# restore_kueue_fixtures runs here via the EXIT trap, resetting the local FS without re-pushing.
