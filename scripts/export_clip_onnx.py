#!/usr/bin/env python3
"""Export a CLIP vision tower (pooler_output head) to ONNX for the Go
image_spam_filter builtin.

Exports ONLY the vision tower — the Python module embeds images via
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

Usage (run with the image_spam_filter venv python — needs torch+transformers;
`verify` additionally needs onnxruntime):
    python3 scripts/export_clip_onnx.py export [--variant b32] [--model-dir DIR]
    python3 scripts/export_clip_onnx.py verify [--variant b32] [--model-dir DIR] [--onnx PATH]

`export` writes clip-vision-<variant>.onnx into --model-dir (default:
modules/imagefilter/models/; gitignored). `verify` runs the same image through
torch and onnxruntime and asserts agreement (cosine ~1.0 and matching output
dims). torch+transformers come from any Python env (e.g. pip install torch
transformers onnxruntime onnxscript); the old module's venv was retired with
the Python image_spam_filter (its backup lives in .hermes/backups/).
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

DEFAULT_MODEL_ROOT = "modules/imagefilter/models"  # where the Go builtin reads models from


def variant_dir(variant, model_dir):
    """Weights dir for a variant. Default layout: modules/imagefilter/models
    holds the exported .onnx files (gitignored); the HF torch weights cache
    stays in the HF cache dir — only the ONNX artifact lands in the repo tree."""
    if model_dir:
        return model_dir
    return DEFAULT_MODEL_ROOT


def out_path(variant, model_dir):
    return os.path.join(variant_dir(variant, model_dir), f"clip-vision-{variant}.onnx")


def load_wrapper(model_dir):
    import torch
    from transformers import CLIPModel

    model = CLIPModel.from_pretrained(model_dir)
    model.eval()

    class VisionPooler(torch.nn.Module):
        """vision_model → pooler_output (the exact tensor the Python module uses)."""

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
    model_dir = variant_dir(args.variant, args.model_dir)
    out = out_path(args.variant, args.model_dir)
    px = VARIANTS[args.variant][2]

    if not os.path.isdir(model_dir):
        # One-time weight download for variants not seen before.
        from huggingface_hub import snapshot_download

        snapshot_download(
            VARIANTS[args.variant][0],
            local_dir=model_dir,
            allow_patterns=["*.json", "*.txt", "*.bin"],
        )

    torch.manual_seed(0)
    _, _, wrapper = load_wrapper(model_dir)
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
    model_dir = variant_dir(args.variant, args.model_dir)
    onnx_file = args.onnx or out_path(args.variant, args.model_dir)
    if not os.path.isfile(onnx_file):
        sys.exit(f"missing ONNX file: {onnx_file} (run `export` first)")
    import onnxruntime as ort

    torch.manual_seed(0)
    _, model, _ = load_wrapper(model_dir)
    processor = CLIPProcessor.from_pretrained(model_dir)

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

    pe = sub.add_parser("export", help="export vision tower to ONNX")
    pe.add_argument("--variant", default="b32", help=f"CLIP variant: {', '.join(VARIANTS)} (default: b32)")
    pe.add_argument("--model-dir", default=None, help="weights dir (default: models/<hf-name> under the python module)")
    pe.set_defaults(func=cmd_export)

    pv = sub.add_parser("verify", help="verify ONNX matches torch numerically")
    pv.add_argument("--variant", default="b32", help=f"CLIP variant: {', '.join(VARIANTS)} (default: b32)")
    pv.add_argument("--model-dir", default=None)
    pv.add_argument("--onnx", default=None)
    pv.set_defaults(func=cmd_verify)

    args = p.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
