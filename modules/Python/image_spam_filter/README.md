# image_spam_filter

A module for the Misfit Discord bot that detects and punishes **spam images**
in guild channels using [OpenAI CLIP](https://huggingface.co/openai/clip-vit-base-patch32)
(vision embeddings compared by cosine similarity). CLIP runs **entirely on CPU**,
so `torch` is the CPU wheel and the model weights live inside this module folder.

## Install

The module loader auto-installs the dependencies from `requirements.txt` into the
module's per-module `.venv` on first load (CPU-only torch — no CUDA wheel). On the
first load it also ensures the CLIP model is present:

- If `models/clip-vit-base-patch32/` already contains `pytorch_model.bin`, it is
  used directly (fully offline).
- Otherwise the module downloads `openai/clip-vit-base-patch32` into that folder
  (one-time, ~600 MB).

## Usage

Prefix command (bot owner or a guild owner):

```text
[prefix]spamfilter help
[prefix]spamfilter add            # then send the image(s) you want to mark as spam in this channel
[prefix]spamfilter action <none|kick|ban|mute>
[prefix]spamfilter deleteonnone <true|false>
[prefix]spamfilter mutetime <seconds>
[prefix]spamfilter logchannel [<#channel>]   # empty to disable logging
[prefix]spamfilter threshold <0.0-1.0>
[prefix]spamfilter toggle
[prefix]spamfilter reset
```

When the filter is enabled, every image sent in a guild is embedded with CLIP and
compared against the reference images added via `add`. A cosine similarity above
`threshold` triggers the configured punishment.

## Configuration (per guild, stored in `config.json`)

| Key | Default | Meaning |
|-----|---------|---------|
| `enabled` | `false` | Master switch for the filter in this guild. |
| `threshold` | `0.95` | Similarity (0.0–1.0) above which an image is flagged. |
| `punishment` | `none` | What to do on a match: `none`, `kick`, `ban`, `mute`. |
| `mute_duration` | `600` | Mute/timeout length in seconds. |
| `log_channel` | `null` | Channel to post a detection log (embed with the image). |
| `delete_on_none` | `false` | Also delete the message when `punishment` is `none`. |

## Layout & gitignored runtime data

- `models/clip-vit-base-patch32/` — CLIP weights (~600 MB). Kept in the folder for
  a self-contained/offline module; gitignored and auto-resolved on load.
- `spam_images/` — reference images added via `add`, turned into vectors.
- `config.json` — per-guild settings.
- `.venv/` — Python dependencies.

All of the above are gitignored; only `main.py`, `requirements.txt`, and
`.gitignore` are source.

## Notes / adaptations

This is a port of a RedBot cog. Because this bot's Python SDK differs from Red:

- Subcommands are a single `spamfilter` command that branches on the first arg.
- The owner/guild-owner check and the `add` capture are done manually (the command
  context carries no bot info or attachments — `add` uses a short-lived "pending
  add" window answered by the next image in the channel).
- The log embed points at the attachment's public CDN URL for its image, since
  this bot's REST layer does not support file uploads.
- The `warning_message` feature from the original cog was removed.
