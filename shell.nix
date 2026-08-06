# Declarative dependency resolution for Misfit Bot (Nix / NixOS).
#
#   nix-shell --run './install.sh --no-deps'
#
# Provides: Go toolchain, C compiler (cgo), pkg-config + opus/opusfile
# (gopkg.in/hraban/opus.v2 is a cgo binding: #cgo pkg-config: opus),
# git (self-updater), python3 (Python modules), ffmpeg (voice playback).
{ pkgs ? import <nixpkgs> { } }:

pkgs.mkShell {
  buildInputs = with pkgs; [
    go
    gcc
    pkg-config
    opus
    opusfile
    git
    python3
    ffmpeg
  ];
}
