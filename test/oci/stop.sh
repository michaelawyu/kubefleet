#!/usr/bin/env bash

set -euo pipefail

CONTAINER_NAME="local-registry"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AUTH_DIR="${SCRIPT_DIR}/auth"
OUTPUT_DIR="${SCRIPT_DIR}/output"

if docker ps --format '{{.Names}}' | grep -qx "${CONTAINER_NAME}"; then
	docker stop "${CONTAINER_NAME}" >/dev/null
	echo "Stopped local registry container: ${CONTAINER_NAME}"
elif docker ps -a --format '{{.Names}}' | grep -qx "${CONTAINER_NAME}"; then
	echo "Local registry container is already stopped: ${CONTAINER_NAME}"
else
	echo "Local registry container not found: ${CONTAINER_NAME}"
fi

if [ -d "${AUTH_DIR}" ]; then
	rm -rf "${AUTH_DIR}"
	echo "Removed auth directory: ${AUTH_DIR}"
else
	echo "Auth directory not found: ${AUTH_DIR}"
fi

if [ -d "${OUTPUT_DIR}" ]; then
	rm -rf "${OUTPUT_DIR}"
	echo "Removed output directory: ${OUTPUT_DIR}"
else
	echo "Output directory not found: ${OUTPUT_DIR}"
fi
