#!/usr/bin/env bash
# setup_imagefilter.sh — make the ONNX image-spam-filter runtime ready.
#
# Installs BOTH artifacts the compiled-in imagefilter builtin needs at runtime:
#   1. lib/onnxruntime/lib/libonnxruntime.so   (via scripts/setup_onnx.sh)
#   2. modules/imagefilter/models/clip-vision-b32.onnx  (CPU CLIP vision tower)
#
# Then proves the export with the export script's `verify` subcommand (torch vs
# onnxruntime cosine on the same input). Everything heavy happens ONCE: the
# venv and the .onnx are reused on every later run, so calling this after each
# self-update costs nothing.
#
# It is called by install.sh (fresh install) and by the updater after a binary
# swap, and can be run by hand for a non-default variant (see
# scripts/export_clip_onnx.py). Non-interactive: no prompts, ever.
#
# Usage:
#   bash scripts/setup_imagefilter.sh             # install what is missing (never fails hard)
#   bash scripts/setup_imagefilter.sh --strict    # exit 1 when an artifact cannot be built
#   bash scripts/setup_imagefilter.sh --verify    # also re-run `verify` (needs the export venv)
#   bash scripts/setup_imagefilter.sh --check     # report readiness, change nothing
#   bash scripts/setup_imagefilter.sh --force     # re-export even if the .onnx exists
#   bash scripts/setup_imagefilter.sh --clean     # delete the export venv, keep artifacts
#
# Default mode is deliberately non-fatal: the filter is an optional runtime
# feature (it stays cold with the reason on the dashboard), so a failed
# artifact build must never break an install or roll back an update.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
MODELS_DIR="$ROOT/modules/imagefilter/models"
VENV="$MODELS_DIR/.export-venv"
VARIANT="b32"
MODEL_FILE="$MODELS_DIR/clip-vision-${VARIANT}.onnx"
# Cheap "is this a real export?" floor in bytes — the b32 export is ~334 MiB;
# anything below 100 MiB is a truncated/failed download, not a model.
MIN_MODEL_BYTES=$((100 * 1024 * 1024))

# CPU-only torch: the CPU wheel index carries torch and its whole dep set, so a
# single index URL installs a ~200 MB CPU wheel instead of the multi-GB CUDA one.
TORCH_INDEX="https://download.pytorch.org/whl/cpu"
PYPI_INDEX="https://pypi.org/simple"
# Everything the export + verify path imports (torch comes from TORCH_INDEX).
EXPORT_PKGS="transformers onnx onnxscript onnxruntime numpy pillow"

STRICT=0
FORCE=0
CHECK=0
CLEAN=0
DO_VERIFY=0

info() { printf '\033[1;34m[ifilter]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[ ok    ]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[ warn  ]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[ fail  ]\033[0m %s\n' "$*" >&2; exit 1; }

# fail() = die in --strict, warn-and-continue otherwise.
fail() {
  if [ "$STRICT" = 1 ]; then die "$*"; fi
  warn "$*"
  return 0
}

for arg in "$@"; do
  case "$arg" in
    --strict) STRICT=1 ;;
    --verify) DO_VERIFY=1 ;;
    --force)  FORCE=1 ;;
    --check)  CHECK=1 ;;
    --clean)  CLEAN=1 ;;
    -h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument: $arg (see --help)" ;;
  esac
done

have() { command -v "$1" >/dev/null 2>&1; }

lib_present() { [ -f "$ROOT/lib/onnxruntime/lib/libonnxruntime.so" ]; }

model_present() {
  [ -f "$MODEL_FILE" ] || return 1
  local size
  size=$(stat -c %s "$MODEL_FILE" 2>/dev/null || echo 0)
  [ "$size" -ge "$MIN_MODEL_BYTES" ]
}

model_size_mb() {
  local size
  size=$(stat -c %s "$MODEL_FILE" 2>/dev/null || echo 0)
  echo $((size / 1024 / 1024))
}

# ── check mode ────────────────────────────────────────────────────────────

if [ "$CHECK" = 1 ]; then
  info "check mode — nothing will be installed"
  if lib_present; then ok "onnxruntime lib: $ROOT/lib/onnxruntime/lib/libonnxruntime.so"
  else warn "onnxruntime lib: MISSING (run scripts/setup_onnx.sh)"; fi
  if model_present; then ok "CLIP model (${VARIANT}): $MODEL_FILE ($(model_size_mb) MB)"
  elif [ -f "$MODEL_FILE" ]; then warn "CLIP model (${VARIANT}): present but too small — likely truncated"
  else warn "CLIP model (${VARIANT}): MISSING"; fi
  if [ -x "$VENV/bin/python" ]; then ok "export venv: $VENV"
  else info "export venv: absent (only needed to build a model)"; fi
  exit 0
fi

if [ "$CLEAN" = 1 ]; then
  if [ -d "$VENV" ]; then
    info "removing export venv $VENV"
    rm -rf "$VENV"
    ok "export venv removed (artifacts kept)"
  else
    info "no export venv at $VENV"
  fi
fi

# ── 1. ONNX Runtime C library ─────────────────────────────────────────────

if lib_present && [ "$FORCE" != 1 ]; then
  ok "onnxruntime lib already present"
else
  info "installing onnxruntime C library (scripts/setup_onnx.sh)"
  if bash "$ROOT/scripts/setup_onnx.sh"; then
    lib_present && ok "onnxruntime lib ready" || fail "setup_onnx.sh ran but the library is still missing"
  else
    fail "could not install the onnxruntime C library — the image filter stays cold until it is present"
  fi
fi

# ── 2. CLIP b32 model ─────────────────────────────────────────────────────

if model_present && [ "$FORCE" != 1 ]; then
  ok "CLIP model (${VARIANT}) already present ($(model_size_mb) MB) — nothing to build"
  if [ "$DO_VERIFY" = 1 ]; then
    if [ -x "$VENV/bin/python" ]; then
      info "re-running verify (--verify)"
      if ! "$VENV/bin/python" "$ROOT/scripts/export_clip_onnx.py" verify --variant "$VARIANT" --out-dir "$MODELS_DIR"; then
        fail "verify failed for the existing ${VARIANT} model — re-run with --force to rebuild it"
      fi
    else
      warn "--verify needs the export venv, which is absent — skipping (run without --clean to keep it)"
    fi
  fi
  ok "image filter runtime ready"
  exit 0
fi

[ "$FORCE" = 1 ] && info "forced rebuild of the ${VARIANT} model" || true

# ── interpreter selection ─────────────────────────────────────────────────
#
# CPython 3.14 has no plain (GIL-enabled) wheels for onnx/tokenizers today, so
# prefer a slightly older interpreter when one is installed, and let pip decide:
# each candidate is probed with a resolvability dry-run of the exact stack.
# Nothing but the winner gets a venv.

probe_interpreter() { # $1 = interpreter name; 0 = stack resolves
  local py="$1" tmp
  have "$py" || return 1
  "$py" -c 'import sys; sys.exit(0 if sys.version_info >= (3, 9) else 1)' 2>/dev/null || return 1
  "$py" -m venv --help >/dev/null 2>&1 || return 1
  tmp=$(mktemp -d)
  if "$py" -m pip install --dry-run --quiet --ignore-installed --only-binary=:all: \
        --target "$tmp" --index-url "$PYPI_INDEX" \
        --extra-index-url "$TORCH_INDEX" $EXPORT_PKGS torch >/dev/null 2>&1; then
    rm -rf "$tmp"
    return 0
  fi
  rm -rf "$tmp"
  return 1
}

choose_interpreter() {
  # 3.13/3.12/3.11 first (widest wheel coverage), then whatever python3 is.
  local cand
  for cand in python3.13 python3.12 python3.11 python3; do
    if probe_interpreter "$cand"; then
      echo "$cand"
      return 0
    fi
  done
  return 1
}

need_venv=1
if [ -x "$VENV/bin/python" ] && [ "$FORCE" != 1 ]; then
  ok "reusing export venv $VENV"
  need_venv=0
fi

if [ "$need_venv" = 1 ]; then
  info "looking for a Python that can install the export stack (torch/transformers/onnx)"
  interp=$(choose_interpreter) || fail \
    "no Python on this host can install torch+transformers+onnx — need python3.12/3.13, or python3.14 with wheels published for it. The image filter stays cold; the bot is unaffected."
  if [ -n "${interp:-}" ]; then
    info "using $interp ($("$interp" --version 2>&1))"
    rm -rf "$VENV"
    mkdir -p "$MODELS_DIR"
    if ! "$interp" -m venv "$VENV"; then
      fail "could not create the export venv at $VENV"
    fi
    # torch first, from the CPU index only — this is what keeps the install
    # ~200 MB instead of pulling the CUDA build off PyPI.
    info "installing torch (CPU) — this is the big one-time download"
    if ! PIP_DISABLE_PIP_VERSION_CHECK=1 "$VENV/bin/python" -m pip install --quiet \
          --retries 5 --timeout 60 --only-binary=:all: \
          --index-url "$TORCH_INDEX" torch; then
      fail "could not install CPU torch into the export venv"
    fi
    info "installing transformers + onnx toolchain"
    if ! PIP_DISABLE_PIP_VERSION_CHECK=1 "$VENV/bin/python" -m pip install --quiet \
          --retries 5 --timeout 60 --only-binary=:all: \
          --index-url "$PYPI_INDEX" $EXPORT_PKGS; then
      fail "could not install the export dependencies into the export venv"
    fi
    ok "export venv ready at $VENV"
  fi
fi

# A venv exists from a previous run but may be broken (e.g. interrupted
# install); the export step below reports that clearly if so.
if [ ! -x "$VENV/bin/python" ]; then
  fail "export venv is unavailable — cannot build the ${VARIANT} model. Install python3-venv/pip or run with --clean and retry."
  ok "image filter lib ready; model missing (filter stays cold)"
  exit 0
fi

# ── 3. export + verify ────────────────────────────────────────────────────

info "exporting the CLIP ${VARIANT} vision tower to ONNX (downloads HF weights once)"
if ! "$VENV/bin/python" "$ROOT/scripts/export_clip_onnx.py" export \
      --variant "$VARIANT" --out-dir "$MODELS_DIR"; then
  fail "export failed — the image filter stays cold until a model is built"
  exit 0
fi

if ! model_present; then
  fail "export reported success but $MODEL_FILE is missing or truncated"
  exit 0
fi
ok "model: $MODEL_FILE ($(model_size_mb) MB)"

info "verifying the export (torch vs onnxruntime cosine)"
if "$VENV/bin/python" "$ROOT/scripts/export_clip_onnx.py" verify \
     --variant "$VARIANT" --out-dir "$MODELS_DIR"; then
  ok "verify PASS — image filter runtime ready"
else
  fail "verify failed — the ONNX export does not match torch; rebuild with --force and check the log"
fi

exit 0