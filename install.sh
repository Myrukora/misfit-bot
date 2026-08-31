#!/usr/bin/env bash
# install.sh — Misfit Bot: system dependencies + build (single binary; Lua/Python modules dynamic)
#
# Usage:
#   ./install.sh                 # detect distro, install deps, build single binary
#   ./install.sh --check         # print detected toolchain/deps, change nothing
#   ./install.sh --no-deps       # skip system packages, only build
#   ./install.sh --skip-go       # don't auto-install the Go toolchain
#   DISTRO=ubuntu ./install.sh   # override distro detection
#
# Note: package installs are always non-interactive; sudo prompts for the
# password itself when needed.
#
# Dependency map (verified against the codebase):
#   build  : Go >= 1.26.4  (go.mod directive; older Go auto-downloads the
#                           toolchain via GOTOOLCHAIN=auto on build)
#            C compiler (cgo), pkg-config,
#            libopus-dev + libopusfile-dev — gopkg.in/hraban/opus.v2 is a cgo
#            binding: #cgo pkg-config: opus
#   runtime: git  — self-updater (updater/ pulls the repo)
#            python3 + venv + pip — Python module system (per-module .venv)
#            ffmpeg — voice playback (modules/voice.go exec.LookPath("ffmpeg"))
#
# Declarative alternative for Nix/NixOS: `nix-shell --run './install.sh --no-deps'`
# (see shell.nix in this directory).

set -euo pipefail

REQUIRED_GO="1.26.4"
# Newest 1.26.x available on go.dev (>= REQUIRED_GO) — pinned with SHA256
# checksums from https://go.dev/dl/?mode=json (verified at install time).
GO_DOWNLOAD="1.26.5"
GO_TARBALL_SHA256_amd64="5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053"
GO_TARBALL_SHA256_arm64="fe4789e92b1f33358680864bbe8704289e7bb5fc207d80623c308935bd696d49"
GO_BASE_URL="https://go.dev/dl"

DO_DEPS=1
DO_GO=1
DO_CHECK=0

info() { printf '\033[1;34m[install]\033[0m %s\n' "$*"; }
ok()    { printf '\033[1;32m[ ok    ]\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m[ warn  ]\033[0m %s\n' "$*" >&2; }
die()   { printf '\033[1;31m[ fail  ]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
}

for arg in "$@"; do
  case "$arg" in
    --no-deps)            DO_DEPS=0 ;;
    --skip-go)            DO_GO=0 ;;
    --check)              DO_CHECK=1 ;;
    -h|--help)            usage ;;
    *) die "unknown argument: $arg (see --help)" ;;
  esac
done

# ── helpers ───────────────────────────────────────────────────────────────

ver_ge() { # dotted-numeric comparison: $1 >= $2 — pure bash, no sort -V
          # dependency (busybox sort on Alpine lacks -V)
  local -a a b
  local i
  IFS=. read -r -a a <<< "$1"
  IFS=. read -r -a b <<< "$2"
  for i in 0 1 2 3; do
    [ "${a[$i]:-0}" -gt "${b[$i]:-0}" ] && return 0
    [ "${a[$i]:-0}" -lt "${b[$i]:-0}" ] && return 1
  done
  return 0
}

have() { command -v "$1" >/dev/null 2>&1; }

sudo_run() { # run a command as root (via sudo when not root)
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    have sudo || die "need root or sudo to install system packages (or use --no-deps)"
    sudo "$@"
  fi
}

detect_distro() {
  [ -n "${DISTRO:-}" ] && { printf '%s\n' "$DISTRO"; return; }
  if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    printf '%s\n' "${ID:-unknown}"
  else
    case "$(uname -s)" in
      FreeBSD) printf '%s\n' freebsd ;;
      *)       printf '%s\n' unknown ;;
    esac
  fi
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  printf 'amd64' ;;
    aarch64|arm64) printf 'arm64' ;;
    *) die "unsupported architecture: $(uname -m)" ;;
  esac
}

family_of() { # distro id -> package family
  case "$1" in
    debian|ubuntu|linuxmint|pop|elementary|zorin|kali|raspbian|devuan) echo apt ;;
    arch|manjaro|endeavouros|garuda|artix|archarm|cachyos)             echo pacman ;;
    fedora)                                                             echo dnf ;;
    rhel|rocky|alma|centos|ol|amzn)                                     echo dnf-rhel ;;
    opensuse|opensuse-leap|opensuse-tumbleweed|suse)                    echo zypper ;;
    alpine)                                                             echo apk ;;
    void)                                                               echo xbps ;;
    gentoo)                                                             echo emerge ;;
    nixos)                                                              echo nix ;;
    freebsd)                                                            echo pkg ;;
    *) echo unknown ;;
  esac
}

pm() { # package-manager non-interactive flags
  case "$1" in
    apt)    echo apt-get ;;
    pacman) echo pacman ;;
    dnf)    echo dnf ;;
    zypper) echo zypper ;;
    apk)    echo apk ;;
    xbps)   echo xbps-install ;;
    pkg)    echo pkg ;;
    *)      echo "$1" ;;
  esac
}

# ── dependency installation (per package family) ─────────────────────────

install_deps() {
  local distro family
  distro="$(detect_distro)"
  family="$(family_of "$distro")"
  info "distro: ${distro} (family: ${family})"

  # nix users get the declarative route — no imperative package install.
  if [ "$family" = nix ]; then
    warn "NixOS detected: use the declarative route instead:"
    warn "    nix-shell --run './install.sh --no-deps'   (uses shell.nix)"
    die "run inside nix-shell, or set --no-deps after preparing deps manually"
  fi

  case "$family" in
    apt)
      sudo_run apt-get update
      sudo_run apt-get install -y \
        pkg-config libopus-dev libopusfile-dev build-essential \
        git python3 python3-venv python3-pip ffmpeg
      ;;
    pacman)
      # Full system upgrade is intentional: Arch does not support partial
      # upgrades (old+new package mixes can break libs, incl. the opus cgo
      # build). Safe to skip with --no-deps if you update the system yourself.
      sudo_run pacman -Syu --needed --noconfirm \
        pkgconf opus opusfile base-devel git python python-pip ffmpeg
      ;;
    dnf|dnf-rhel)
      if [ "$family" = dnf-rhel ]; then
        info "enabling EPEL (needed for ffmpeg on RHEL-family)"
        sudo_run dnf install -y epel-release
      fi
      sudo_run dnf groupinstall -y "Development Tools" || \
        sudo_run dnf group install -y "Development Tools"
      sudo_run dnf install -y \
        pkgconf-pkg-config opus-devel opusfile-devel git python3 python3-pip
      # ffmpeg: `ffmpeg` on RHEL-family (EPEL), `ffmpeg-free` on newer Fedora.
      if ! sudo_run dnf install -y ffmpeg 2>/dev/null; then
        if ! sudo_run dnf install -y ffmpeg-free 2>/dev/null; then
          warn "could not install ffmpeg (voice playback will be unavailable)"
        fi
      fi
      ;;
    zypper)
      sudo_run zypper --non-interactive install \
        pkg-config libopus-devel libopusfile-devel \
        gcc git python3 python3-pip ffmpeg
      ;;
    apk)
      sudo_run apk add --no-cache \
        pkgconf opus-dev opusfile-dev build-base \
        git python3 py3-pip py3-virtualenv ffmpeg
      ;;
    xbps)
      sudo_run xbps-install -Syu
      sudo_run xbps-install -y \
        pkg-config opus-devel opusfile-devel base-devel \
        git python3 python3-pip ffmpeg
      ;;
    emerge)
      sudo_run emerge --ask=n \
        media-libs/opus media-libs/opusfile dev-util/pkgconf sys-devel/gcc \
        dev-vcs/git dev-lang/python media-video/ffmpeg
      ;;
    pkg) # FreeBSD
      sudo_run pkg install -y \
        pkgconf opus opusfile git go python3 ffmpeg
      ;;
    *)
      cat >&2 <<EOF
[ fail  ] unsupported distro '${distro}' — install manually:
    pkg-config + libopus-dev + libopusfile-dev  (build: cgo opus binding)
    C toolchain (gcc/clang), git, python3 + venv + pip, ffmpeg
    Go >= ${REQUIRED_GO} (https://go.dev/dl)
    then re-run:  ./install.sh --no-deps
EOF
      exit 1
      ;;
  esac
}

# ── Go toolchain ──────────────────────────────────────────────────────────

ensure_go() {
  if have go; then
    local have_go
    have_go="$(go version | sed -E 's/.*go([0-9.]+).*/\1/')"
    if ver_ge "$have_go" "$REQUIRED_GO"; then
      ok "Go ${have_go} (>= ${REQUIRED_GO})"
      return
    fi
    warn "Go ${have_go} is older than ${REQUIRED_GO} — the build will auto-download"
    warn "a newer toolchain (GOTOOLCHAIN=auto). If that fails, install Go manually."
    return
  fi

  [ "$DO_GO" = 1 ] || die "Go is not installed (use --skip-go only if you provide your own toolchain)"

  local arch url tmp sha256 varname
  arch="$(detect_arch)"
  url="${GO_BASE_URL}/go${GO_DOWNLOAD}.linux-${arch}.tar.gz"
  varname="GO_TARBALL_SHA256_${arch}"
  sha256="${!varname:-}"
  info "Go not found — downloading ${url}"
  tmp="$(mktemp -d)"
  if have curl; then
    curl -fsSL "$url" -o "$tmp/go.tgz" || die "download failed — install Go manually from ${GO_BASE_URL}"
  elif have wget; then
    wget -q "$url" -O "$tmp/go.tgz" || die "download failed — install Go manually from ${GO_BASE_URL}"
  else
    die "need curl or wget to download Go — install Go manually from ${GO_BASE_URL}"
  fi
  if [ -n "$sha256" ]; then
    # Verify the tarball before touching /usr/local.
    if ! printf '%s  %s\n' "$sha256" "$tmp/go.tgz" | sha256sum -c - >/dev/null 2>&1; then
      rm -rf "$tmp"
      die "Go tarball checksum mismatch (expected ${sha256}) — aborting; install Go manually from ${GO_BASE_URL}"
    fi
    ok "Go tarball checksum verified"
  else
    warn "no pinned checksum for arch ${arch} — skipping verification"
  fi
  sudo_run rm -rf /usr/local/go
  sudo_run tar -C /usr/local -xzf "$tmp/go.tgz"
  rm -rf "$tmp"
  export PATH="/usr/local/go/bin:$PATH"
  ok "Go ${GO_DOWNLOAD} installed at /usr/local/go"
}

# ── runtime binary sanity (warn-only: features degrade, build still works) ─

check_runtime() {
  have git    || warn "git not found — self-updater ([p]update) will not work"
  have python3 || warn "python3 not found — Python modules will not load"
  have ffmpeg || warn "ffmpeg not found — voice playback will not work"
  if have pkg-config; then
    pkg-config --exists opus     && ok "pkg-config + libopus present" \
                                 || warn "libopus dev files not found (cgo build will fail)"
  else
    warn "pkg-config not found (cgo build will fail)"
  fi
}

# ── build ─────────────────────────────────────────────────────────────────

build_core() {
  info "building core binary (./cmd/bot)"
  # -X main.Version is stamped from the VERSION file (single source of truth).
  # scripts/version.sh is the one parser for it, shared with CI, the release
  # workflow and updater.ReadVersionFile.
  local root
  root=$(cd "$(dirname "$0")" && pwd)
  ( cd "$root" && CGO_ENABLED=1 go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/ )
  ok "core binary: ./bot"
}

# ── main ──────────────────────────────────────────────────────────────────

if [ "$DO_CHECK" = 1 ]; then
  info "check mode — nothing will be installed or built"
  have go && info "go:    $(go version)" || warn "go: NOT INSTALLED"
  have git && info "git:   $(git --version)" || warn "git: not found"
  have python3 && info "python3: $(python3 --version 2>&1)" || warn "python3: not found"
  have ffmpeg && info "ffmpeg: $(ffmpeg -version 2>/dev/null | head -n1)" || warn "ffmpeg: not found"
  have pkg-config && info "pkg-config: $(pkg-config --version)" && info "libopus:    $(pkg-config --modversion opus 2>/dev/null || echo 'opus NOT found')" || warn "pkg-config: not found"
  exit 0
fi

if [ "$DO_DEPS" = 1 ]; then
  install_deps
else
  info "skipping system dependency installation (--no-deps)"
fi

ensure_go
check_runtime
build_core

cat <<EOF

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
 ✅ Build complete.
    core:   ./bot (single binary — dashboard + feature modules compiled in)
    modules: Lua/Python loaded dynamically at runtime

 Next steps:
   1. Run the bot:  ./bot          (first run starts the onboarding wizard)
   2. In Discord:   [p]update status
   3. Notifications: [p]update set notify_channel <channel-id>
   4. Preview embeds: [p]update test
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
EOF
