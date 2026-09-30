#!/usr/bin/env bash
# Download the ONNX Runtime C library (CPU) for the imagefilter builtin.
#
# Idempotent: skips when lib/onnxruntime/lib/libonnxruntime.so already exists.
# The library is gitignored (lib/) — run this once per machine (dev box and
# server). The Go binary loads it at runtime via dlopen (yalue/onnxruntime_go),
# so the build itself never needs it.
#
# Usage: bash scripts/setup_onnx.sh
#
# Called by scripts/setup_imagefilter.sh; run it directly only to fetch the
# library without building a model.
set -euo pipefail

ORT_VERSION="1.30.0"
ORT_DIR="lib/onnxruntime"

case "$(uname -m)" in
    x86_64|amd64)  ORT_ARCH="x64" ;;
    aarch64|arm64) ORT_ARCH="aarch64" ;;
    *)             echo "unsupported architecture: $(uname -m) — install onnxruntime manually into ${ORT_DIR}/lib" >&2; exit 1 ;;
esac

TGZ="onnxruntime-linux-${ORT_ARCH}-${ORT_VERSION}.tgz"
URL="https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/${TGZ}"

if [ -f "${ORT_DIR}/lib/libonnxruntime.so" ]; then
    echo "onnxruntime ${ORT_VERSION} already present at ${ORT_DIR} — nothing to do"
    exit 0
fi

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fL --retry 3 --retry-delay 2 -o "$1" "$2"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q --tries=3 -O "$1" "$2"; }
else
    echo "need curl or wget to download ${URL}" >&2
    exit 1
fi

mkdir -p lib
echo "downloading ${URL} ..."
fetch "lib/${TGZ}" "${URL}"

echo "extracting ..."
tar -xzf "lib/${TGZ}" -C lib/
rm -f "lib/${TGZ}"

# Flatten: lib/onnxruntime-linux-<arch>-<ver>/ -> lib/onnxruntime/
rm -rf "${ORT_DIR}"
mv "lib/onnxruntime-linux-${ORT_ARCH}-${ORT_VERSION}" "${ORT_DIR}"

ls -la "${ORT_DIR}/lib/" | head -5
echo "OK: onnxruntime ${ORT_VERSION} (${ORT_ARCH}) ready at ${ORT_DIR}"