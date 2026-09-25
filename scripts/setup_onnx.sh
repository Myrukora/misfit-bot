#!/usr/bin/env bash
# Download the ONNX Runtime C library (CPU) for the imagefilter builtin.
#
# Idempotent: skips when lib/onnxruntime/lib/libonnxruntime.so already exists.
# The library is gitignored (lib/) — run this once per machine (dev box and
# server). The Go binary loads it at runtime via dlopen (yalue/onnxruntime_go),
# so the build itself never needs it.
#
# Usage: bash scripts/setup_onnx.sh
set -euo pipefail

ORT_VERSION="1.30.0"
ORT_DIR="lib/onnxruntime"
TGZ="onnxruntime-linux-x64-${ORT_VERSION}.tgz"
URL="https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/${TGZ}"

if [ -f "${ORT_DIR}/lib/libonnxruntime.so" ]; then
    echo "onnxruntime ${ORT_VERSION} already present at ${ORT_DIR} — nothing to do"
    exit 0
fi

mkdir -p lib
echo "downloading ${URL} ..."
curl -fL --retry 3 -o "lib/${TGZ}" "${URL}"

echo "extracting ..."
tar -xzf "lib/${TGZ}" -C lib/
rm -f "lib/${TGZ}"

# Flatten: lib/onnxruntime-linux-x64-<ver>/ -> lib/onnxruntime/
rm -rf "${ORT_DIR}"
mv "lib/onnxruntime-linux-x64-${ORT_VERSION}" "${ORT_DIR}"

ls -la "${ORT_DIR}/lib/" | head -5
echo "OK: onnxruntime ${ORT_VERSION} ready at ${ORT_DIR}"
