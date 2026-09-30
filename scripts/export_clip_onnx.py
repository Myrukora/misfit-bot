#!/usr/bin/env python3
"""Export a CLIP vision tower (pooler_output head) to ONNX for the Go
imagefilter builtin.

Exports ONLY the vision tower — the Go runtime embeds images via
`model.vision_model(pixel_values).pooler_output` (CLS token, L2-normalized
afterwards), so the text encoder and tokenizers are never needed at runtime.

Variants (openai/clip-*):
    b32      ViT-B/32        224px   768-d   ~334MB   (DEFAULT)
    b16      ViT-B/16        224px   768-d   ~600MB
    l14      ViT-L/14        224px   1024-d  ~1.2GB
    l14-336  ViT-L/14@336px  336px   1024-d  ~1.2GB

The bot's default is b32. Other variants are exported on demand — if the owner
selects a variant in the dashboard whose .onnx file is missing, the filter
reports it rather than silently falling back.

Usage (needs torch+transformers; `verify` additionally needs onnxruntime):
    python3 scripts/export_clip_onnx.py export [--variant b32] [--out-dir DIR] [--weights-dir DIR]
    python3 scripts/export_clip_onnx.py verify [--variant b32] [--out-dir DIR] [--weights-dir DIR] [--onnx PATH]

`export` writes clip-vision-<variant>.onnx into --out-dir, which defaults to
<repo>/modules/imagefilter/models/ — the exact flat directory the Go builtin
reads (`clip.go: modelPath`). That tree is gitignored; only the .onnx artifact
lands there. The HF torch weights are NOT downloaded into the repo: they come
from the standard Hugging Face cache (or --weights-dir when given).

A ready-made environment (venv + deps + libonnxruntime.so + the b32 model) is
produced by scripts/setup_imagefilter.sh; run this script by hand only for a
non-default variant, with any Python env that has torch+transformers+onnxscript.
"""

import argparse
import os
import sys

# tag: (HF repo, pooler dim, input px)
VARIANTS = {
    "b32": ("openai/clip-vit-base-patch32", 768, 224),
    "b16": ("openai/clip-vit-base-patch16", 768, 224),
    "l14": ("openai/clip-vit-large-patch14", 1024, 224),
    "l14-336": ("openai/clip-vit-large-patch14-336", 1024, 336),
}

_REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
# Where the Go builtin reads models from — flat, one file per variant
# (`clip-vision-<variant>.onnx`); see internal/builtin/imagefilter/clip.go.
DEFAULT_OUT_DIR = os.path.join(_REPO_ROOT, "modules", "imagefilter", "models")

# Files a CLIP repo needs for CLIPModel.from_pretrained + CLIPProcessor.
# Some repos ship `model.safetensors`, others the legacy `pytorch_model.bin`.
WEIGHT_PATTERNS = ["*.json", "*.txt", "*.bin", "*.safetensors"]


def out_path(variant, out_dir):
    """Path of the exported ONNX artifact for a variant.

    The layout is FLAT: every variant's file sits directly in the same models
    dir, keyed by filename — that is what the Go runtime opens.
    """
    return os.path.join(out_dir or DEFAULT_OUT_DIR, f"clip-vision-{variant}.onnx")


def weights_path(variant, weights_dir):
    """Ensure the HF weights for a variant are available and return their path.

    Without --weights-dir this uses the shared Hugging Face cache (nothing is
    written into the repository tree); with it, the files are materialised in
    that directory instead.
    """
    from huggingface_hub import snapshot_download

    kwargs = {"allow_patterns": WEIGHT_PATTERNS}
    if weights_dir:
        kwargs["local_dir"] = weights_dir
    return snapshot_download(VARIANTS[variant][0], **kwargs)


def load_wrapper(weights):
    import torch
    from transformers import CLIPModel

    model = CLIPModel.from_pretrained(weights)
    model.eval()

    class VisionPooler(torch.nn.Module):
        """vision_model → pooler_output (the exact tensor the Go runtime uses)."""

        def __init__(self, m):
            super().__init__()
            self.vision_model = m.vision_model

        def forward(self, pixel_values):
            return self.vision_model(pixel_values=pixel_values).pooler_output

    return torch, model, VisionPooler(model)


def cmd_export(args):
    import torch

    if args.variant not in VARIANTS:
        sys.exit(f"unknown variant {args.variant!r}; choose from: {', '.join(VARIANTS)}")
    out = out_path(args.variant, args.out_dir)
    px = VARIANTS[args.variant][2]

    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)

    # One-time weight fetch (shared HF cache unless --weights-dir was given).
    weights = weights_path(args.variant, args.weights_dir)
    print(f"weights: {weights}")

    torch.manual_seed(0)
    _, _, wrapper = load_wrapper(weights)
    dummy = torch.randn(1, 3, px, px)
    torch.onnx.export(
        wrapper,
        dummy,
        out,
        input_names=["pixel_values"],
        output_names=["embedding"],
        dynamic_axes={"pixel_values": {0: "batch"}, "embedding": {0: "batch"}},
        opset_version=17,
        do_constant_folding=True,
        dynamo=False,  # legacy TorchScript exporter: torch 2.13's dynamo path
        # emits a duplicate "embedding" definition that onnxruntime rejects.
    )
    size_mb = os.path.getsize(out) / (1024 * 1024)
    print(f"exported: {out} ({size_mb:.1f} MB)")


def cmd_verify(args):
    import numpy as np
    import torch
    from PIL import Image
    from transformers import CLIPProcessor

    if args.variant not in VARIANTS:
        sys.exit(f"unknown variant {args.variant!r}; choose from: {', '.join(VARIANTS)}")
    _, expect_dim, _ = VARIANTS[args.variant]
    onnx_file = args.onnx or out_path(args.variant, args.out_dir)
    if not os.path.isfile(onnx_file):
        sys.exit(f"missing ONNX file: {onnx_file} (run `export` first)")
    import onnxruntime as ort

    weights = weights_path(args.variant, args.weights_dir)

    torch.manual_seed(0)
    _, model, _ = load_wrapper(weights)
    processor = CLIPProcessor.from_pretrained(weights)

    # Two visually distinct synthetic images: solid red vs solid blue. Real-photo
    # exactness doesn't matter — we compare torch vs onnx on the SAME input.
    imgs = [
        Image.new("RGB", (300, 220), (255, 0, 0)),
        Image.new("RGB", (280, 300), (0, 0, 255)),
    ]

    session = ort.InferenceSession(onnx_file, providers=["CPUExecutionProvider"])
    worst = 1.0
    for img in imgs:
        inputs = processor(images=img, return_tensors="pt")
        pv = inputs["pixel_values"]
        with torch.no_grad():
            ref = model.vision_model(pixel_values=pv).pooler_output.numpy()
        got = session.run(["embedding"], {"pixel_values": pv.numpy()})[0]
        if got.shape != ref.shape or got.shape[-1] != expect_dim:
            sys.exit(f"shape mismatch: onnx {got.shape} vs torch {ref.shape} (want dim {expect_dim})")
        a = ref[0] / np.linalg.norm(ref[0])
        b = got[0] / np.linalg.norm(got[0])
        cos = float(np.dot(a, b))
        worst = min(worst, cos)
        print(f"torch-vs-onnx cosine: {cos:.6f} (shape {got.shape})")

    if worst < 0.999:
        sys.exit(f"FAILED: worst cosine {worst:.6f} < 0.999")
    print(f"PASS: torch and ONNX agree (worst cosine {worst:.6f})")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)

    def add_common(sp):
        sp.add_argument("--variant", default="b32", help=f"CLIP variant: {', '.join(VARIANTS)} (default: b32)")
        sp.add_argument(
            "--out-dir",
            default=None,
            help="dir for the exported .onnx (default: modules/imagefilter/models)",
        )
        sp.add_argument(
            "--weights-dir",
            default=None,
            help="dir to materialise HF weights in (default: the shared HF cache)",
        )

    pe = sub.add_parser("export", help="export vision tower to ONNX")
    add_common(pe)
    pe.set_defaults(func=cmd_export)

    pv = sub.add_parser("verify", help="verify ONNX matches torch numerically")
    add_common(pv)
    pv.add_argument("--onnx", default=None, help="explicit .onnx to check (default: <out-dir>/clip-vision-<variant>.onnx)")
    pv.set_defaults(func=cmd_verify)

    args = p.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
