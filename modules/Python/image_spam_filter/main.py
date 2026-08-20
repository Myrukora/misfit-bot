"""
Image Spam Filter — a module for the CustomBot Discord bot.

Detects and punishes spam images in guild channels using OpenAI's CLIP model
(vision_model embeddings, compared by cosine similarity). CLIP runs entirely on
CPU, so `torch` is the CPU wheel (see requirements.txt) and the model weights
live inside this module folder (``models/clip-vit-base-patch32``) so the module
is self-contained / offline after the one-time download.

This is a port of a RedBot cog. Key adaptations to this bot's Python SDK:
  * No native subcommand groups -> a single `spamfilter` command that branches
    on `ctx.Args[0]`.
  * The command `Context` has no bot info / attachments / references, so the
    owner/guild-owner check and the `add` capture are handled manually (see
    `_is_admin` and the "pending add" flow in the message event).
  * The REST layer has no multipart file-upload, so the log embed points at the
    attachment's public `proxy_url` (a remote embed image) instead of uploading
    the file.

Threading model (important): the runner dispatches commands and events on a
**single** IPC thread (`_message_loop`). That same thread is the only one that
reads `api_response`/`http_response` replies, so any `self.rest.*` call made
from a command/event handler blocks in `ipc.call` waiting for a reply the loop
can't read until it finishes — effectively stalling the whole loop. To keep the
loop responsive, ALL heavy work (CLIP inference, REST punishments, and
reference-vector rebuilds) runs on one dedicated worker thread driven by a
`queue.Queue`. The worker serializes everything, which also keeps torch calls
from racing (the lock only matters during the brief model-bootstrap window).
"""

import json
import os
import queue
import re
import threading
import time
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone
from io import BytesIO
from PIL import Image

import torch
import torch.nn.functional as F
from transformers import CLIPProcessor, CLIPModel

from custombot import Module, Command, SlashCommand

MODEL_NAME = "openai/clip-vit-base-patch32"
MODEL_DIRNAME = "clip-vit-base-patch32"

DEFAULT_CONFIG = {
    "enabled": False,
    "threshold": 0.95,
    "punishment": "none",
    "mute_duration": 600,
    "log_channel": None,
    "delete_on_none": False,
}

IMAGE_EXTS = (".jpg", ".jpeg", ".png")
# Cap on fetched image size (50 MiB) so a hostile/large CDN response can't
# OOM the worker thread.
MAX_IMAGE_BYTES = 50 * 1024 * 1024


class ImageSpamFilter(Module):
    """Cog for detecting and blocking spam images using OpenAI CLIP (CPU only)."""

    name = "image_spam_filter"
    version = "1.0.0"
    description = "Detect and punish spam images using OpenAI CLIP (CPU only)"
    author = "CustomBot"

    def __init__(self):
        # Populated in on_load.
        self.data_dir = ""
        self.config_path = ""
        self.model_dir = ""
        self.owner_id = ""
        self.prefix = ""
        self.rest = None
        self.logger = None

        # CLIP state.
        self.processor = None
        self.model = None
        self.spam_vectors = []
        self.model_loaded = False

        # Serializes torch calls so bootstrap (loader thread) and the worker
        # never invoke it concurrently.
        self._model_lock = threading.Lock()

        # Single worker thread drains this queue and does all heavy work.
        self._queue = queue.Queue()
        self._worker = None
        self._running = False

        # (guild_id, channel_id) -> expiry timestamp, for the `add` capture flow.
        self.pending_add = {}

        # Per-guild config: {"default": {...}, "guilds": {guild_id: {...}}}.
        self.config = {"default": dict(DEFAULT_CONFIG), "guilds": {}}

    # ── Module interface ──────────────────────────────────────────────

    def on_load(self, ctx):
        self.data_dir = ctx.data_dir
        self.owner_id = ctx.owner_id
        self.prefix = ctx.prefix
        self.rest = ctx.rest
        self.logger = ctx.logger
        self.config_path = os.path.join(self.data_dir, "config.json")
        self.model_dir = os.path.join(self.data_dir, "models", MODEL_DIRNAME)
        os.makedirs(self.data_dir, exist_ok=True)

        self._load_config()

        self.logger.info("Image spam filter module loaded. Bootstrapping CLIP model...")
        # Model loading is heavy; do it on a daemon thread so on_load returns
        # immediately. `model_loaded` gates the message event until ready.
        threading.Thread(target=self._bootstrap, daemon=True).start()

        # Heavy-work worker (inference + REST + vector rebuilds), kept off the
        # runner's single IPC thread.
        self._running = True
        self._worker = threading.Thread(target=self._worker_loop, daemon=True)
        self._worker.start()

    def on_unload(self):
        self._running = False
        self._queue.put(None)  # sentinel to wake the worker
        if self._worker:
            self._worker.join(timeout=5)

    def commands(self):
        return [
            Command(
                name="spamfilter",
                description="Detect and punish spam images using CLIP",
                usage="spamfilter <add|action|deleteonnone|mutetime|logchannel|threshold|toggle|reset>",
                category="moderation",
                execute=self.spamfilter_command,
            ),
        ]

    def slash_commands(self):
        return []

    def event_handlers(self):
        return {"guild_message_create": self.on_message}

    # ── Worker ────────────────────────────────────────────────────────

    def _worker_loop(self):
        while self._running:
            task = self._queue.get()
            if task is None:  # shutdown sentinel
                break
            try:
                op = task.get("op") if isinstance(task, dict) else None
                if op == "check":
                    self._do_check(task.get("data") or {})
                elif op == "add":
                    self._do_add(task.get("data") or {})
                elif op == "refresh":
                    self._refresh_spam_vectors()
            except Exception as e:  # noqa: BLE001 - never let the worker die
                self.logger.error(f"worker task failed: {e}")
            finally:
                self._queue.task_done()

    # ── Bootstrap (background thread) ─────────────────────────────────

    def _bootstrap(self):
        try:
            self._ensure_model()
            self._load_model()
            self._refresh_spam_vectors()
            self.model_loaded = True
            self.logger.info("CLIP model loaded successfully on CPU.")
        except Exception as e:  # noqa: BLE001 - report and keep the module loaded
            self.logger.error(f"Failed to bootstrap CLIP model: {e}")

    def _ensure_model(self):
        if os.path.isfile(os.path.join(self.model_dir, "pytorch_model.bin")):
            return
        self.logger.info("CLIP model not found locally — downloading (one-time)...")
        from huggingface_hub import snapshot_download
        # Only pull the files PyTorch CLIP needs; skip unused TF/Flax weights
        # and model cards (all *.json / *.txt / the bin weights are enough).
        snapshot_download(
            MODEL_NAME,
            local_dir=self.model_dir,
            allow_patterns=["*.json", "*.txt", "pytorch_model.bin"],
        )

    def _load_model(self):
        torch.set_num_threads(2)
        self.processor = CLIPProcessor.from_pretrained(self.model_dir)
        self.model = CLIPModel.from_pretrained(self.model_dir)
        self.model.eval()

    # ── Config persistence (tiny built-in JSON store) ─────────────────

    def _load_config(self):
        if os.path.isfile(self.config_path):
            try:
                with open(self.config_path, "r", encoding="utf-8") as f:
                    self.config = json.load(f)
            except Exception:  # noqa: BLE001
                self.config = {"default": dict(DEFAULT_CONFIG), "guilds": {}}
        else:
            self.config = {"default": dict(DEFAULT_CONFIG), "guilds": {}}
            self._save_config()

    def _save_config(self):
        try:
            with open(self.config_path, "w", encoding="utf-8") as f:
                json.dump(self.config, f, indent=2)
        except Exception as e:  # noqa: BLE001
            self.logger.error(f"Failed to save config: {e}")

    def _guild_settings(self, guild_id):
        base = dict(DEFAULT_CONFIG)
        guild = self.config.get("guilds", {}).get(guild_id)
        if guild:
            base.update(guild)
        return base

    def _set_guild_setting(self, guild_id, key, value):
        guilds = self.config.setdefault("guilds", {})
        if guild_id not in guilds:
            guilds[guild_id] = {}
        guilds[guild_id][key] = value
        self._save_config()

    # ── Embeddings ────────────────────────────────────────────────────

    def _compute_embedding_sync(self, image_bytes):
        """Compute an L2-normalized CLIP embedding for the given bytes.

        Guarded by a lock so torch is never invoked concurrently (bootstrap +
        the worker may otherwise race). Runs on the worker thread.
        """
        with self._model_lock:
            try:
                image = Image.open(BytesIO(image_bytes)).convert("RGB")
                inputs = self.processor(images=image, return_tensors="pt")
                pixel_values = inputs["pixel_values"]
                with torch.no_grad():
                    # vision_model + pooler_output ([CLS] token) — avoids
                    # version-dependent get_image_features internals.
                    outputs = self.model.vision_model(pixel_values=pixel_values)
                    features = outputs.pooler_output
                    features = F.normalize(features, p=2, dim=-1)
                return features.detach().to("cpu")
            except Exception as e:  # noqa: BLE001
                self.logger.error(f"Error processing image: {e}")
                return None

    def _refresh_spam_vectors(self):
        spam_dir = os.path.join(self.data_dir, "spam_images")
        self.spam_vectors = []
        os.makedirs(spam_dir, exist_ok=True)
        for filename in os.listdir(spam_dir):
            if filename.lower().endswith(IMAGE_EXTS):
                with open(os.path.join(spam_dir, filename), "rb") as f:
                    img_bytes = f.read()
                vector = self._compute_embedding_sync(img_bytes)
                if vector is not None:
                    self.spam_vectors.append(vector)
        self.logger.info(f"Refreshed spam vectors. Count: {len(self.spam_vectors)}")

    def _check_similarity(self, image_bytes):
        if not self.spam_vectors:
            return 0.0
        # Runs on the worker thread; the lock keeps it safe during bootstrap.
        new_vector = self._compute_embedding_sync(image_bytes)
        if new_vector is None:
            return 0.0
        # Both vectors are L2-normalized: cosine similarity == dot product.
        max_similarity = 0.0
        for spam_vector in self.spam_vectors:
            similarity = (new_vector * spam_vector).sum().item()
            if similarity > max_similarity:
                max_similarity = similarity
        return max_similarity

    # ── HTTP / attachments ────────────────────────────────────────────

    def _fetch_bytes(self, url):
        # CLIP needs bytes, not URLs. proxy_url is a public Discord CDN URL.
        # Only allow HTTPS requests to Discord's CDN (SSRF protection) and cap
        # the read so a large/hostile response can't exhaust memory.
        parsed = urllib.parse.urlparse(url)
        host = parsed.hostname or ""
        # Match the bare host or any subdomain of Discord's CDN. The dot matters:
        # without it, "evildiscordapp.com" would wrongly pass endswith().
        if (
            parsed.scheme != "https"
            or not host
            or not (host == "discordapp.com" or host.endswith(".discordapp.com"))
        ):
            raise ValueError(f"Rejected non-Discord-HTTPS URL: {url!r}")
        with urllib.request.urlopen(url, timeout=10) as resp:
            return resp.read(MAX_IMAGE_BYTES)

    # ── Permission helpers ────────────────────────────────────────────

    def _is_admin(self, ctx):
        """Bot owner OR guild owner (the SDK has no custom-check decorator)."""
        if ctx.author_id == self.owner_id:
            return True
        if ctx.guild_id:
            try:
                guild = self.rest.get_guild(ctx.guild_id)
                if guild and guild.get("owner_id") == ctx.author_id:
                    return True
            except Exception:  # noqa: BLE001
                pass
        return False

    # ── The spamfilter command (subcommand branching) ─────────────────

    def spamfilter_command(self, ctx):
        if not ctx.guild_id:
            ctx.reply_text("This command must be used in a guild.")
            return
        if not self._is_admin(ctx):
            ctx.respond("🚫 Permission Denied", "Only the bot owner or a guild owner can use this command.")
            return

        sub = ctx.args[0].lower() if ctx.args else ""
        args = ctx.args[1:]

        if sub in ("", "help"):
            self._help(ctx)
        elif sub == "add":
            self._cmd_add(ctx)
        elif sub == "action":
            self._cmd_action(ctx, args)
        elif sub == "deleteonnone":
            self._cmd_deleteonnone(ctx, args)
        elif sub == "mutetime":
            self._cmd_mutetime(ctx, args)
        elif sub == "logchannel":
            self._cmd_logchannel(ctx, args)
        elif sub == "threshold":
            self._cmd_threshold(ctx, args)
        elif sub == "toggle":
            self._cmd_toggle(ctx)
        elif sub == "reset":
            self._cmd_reset(ctx)
        else:
            ctx.respond("⚠️ Usage", f"Unknown subcommand `{sub}`. Use `spamfilter help`.")

    def _help(self, ctx):
        text = (
            "🛡️ Image Spam Filter commands:\n"
            "`add` — Mark image(s) as spam (send them in this channel within 30s, several at once)\n"
            "`action <none|kick|ban|mute>` — Punishment on a match\n"
            "`deleteonnone <true|false>` — Delete the message when punishment is none\n"
            "`mutetime <seconds>` — Mute duration in seconds\n"
            "`logchannel [<#id>]` — Log channel (empty to disable)\n"
            "`threshold <0.0-1.0>` — Similarity threshold\n"
            "`toggle` — Enable/disable the filter\n"
            "`reset` — Clear the spam image database"
        )
        ctx.reply_text(text)

    def _cmd_add(self, ctx):
        # Tie the capture to the requesting user so only they can supply the
        # reference image(s); several attachments in one message are all added.
        key = (ctx.guild_id, ctx.channel_id, ctx.author_id)
        self.pending_add[key] = time.time() + 30
        ctx.reply_text(
            "Send the image(s) you want to mark as spam in this channel within "
            "30 seconds (you can attach several at once)."
        )

    def _cmd_action(self, ctx, args):
        if not args:
            ctx.respond("⚠️ Usage", "spamfilter action <none|kick|ban|mute>")
            return
        action = args[0].lower()
        if action not in ("none", "kick", "ban", "mute"):
            ctx.respond("❌ Error", "Invalid action. Use none, kick, ban, or mute.")
            return
        self._set_guild_setting(ctx.guild_id, "punishment", action)
        ctx.respond("✅ Punishment", f"**{action.upper()}**")

    def _cmd_deleteonnone(self, ctx, args):
        if not args:
            ctx.respond("⚠️ Usage", "spamfilter deleteonnone <true|false>")
            return
        value = args[0].lower() in ("true", "1", "yes", "on")
        self._set_guild_setting(ctx.guild_id, "delete_on_none", value)
        state = "Delete" if value else "Keep"
        ctx.respond("✅ Action on 'None'", f"**{state}** message.")

    def _cmd_mutetime(self, ctx, args):
        if not args:
            ctx.respond("⚠️ Usage", "spamfilter mutetime <seconds>")
            return
        try:
            seconds = int(args[0])
        except ValueError:
            ctx.respond("❌ Error", "Seconds must be an integer.")
            return
        if seconds <= 0:
            ctx.respond("❌ Error", "Seconds must be positive.")
            return
        self._set_guild_setting(ctx.guild_id, "mute_duration", seconds)
        ctx.respond("✅ Mute duration", f"{seconds} s.")

    def _cmd_logchannel(self, ctx, args):
        if not args:
            self._set_guild_setting(ctx.guild_id, "log_channel", None)
            ctx.respond("✅ Logging", "Logging disabled.")
            return
        channel_id = self._extract_channel_id(args[0])
        self._set_guild_setting(ctx.guild_id, "log_channel", channel_id)
        ctx.respond("✅ Logs", f"Logging to channel {channel_id}.")

    def _cmd_threshold(self, ctx, args):
        if not args:
            ctx.respond("⚠️ Usage", "spamfilter threshold <0.0-1.0>")
            return
        try:
            value = float(args[0])
        except ValueError:
            ctx.respond("❌ Error", "Threshold must be a number.")
            return
        if not (0 < value <= 1):
            ctx.respond("❌ Error", "Threshold must be between 0 and 1.")
            return
        self._set_guild_setting(ctx.guild_id, "threshold", value)
        ctx.respond("✅ Threshold", f"`{value}`")

    def _cmd_toggle(self, ctx):
        settings = self._guild_settings(ctx.guild_id)
        new_value = not settings["enabled"]
        self._set_guild_setting(ctx.guild_id, "enabled", new_value)
        ctx.respond("✅ Filter", f"**{'ON' if new_value else 'OFF'}**")

    def _cmd_reset(self, ctx):
        # File deletion is cheap and done inline; the (heavy) vector rebuild
        # runs on the worker so the IPC loop is never blocked.
        self._clear_spam_files()
        self._queue.put({"op": "refresh"})
        ctx.respond("✅ Database", "Spam image database cleared.")

    def _clear_spam_files(self):
        spam_dir = os.path.join(self.data_dir, "spam_images")
        if os.path.isdir(spam_dir):
            for filename in os.listdir(spam_dir):
                if filename.lower().endswith(IMAGE_EXTS):
                    try:
                        os.remove(os.path.join(spam_dir, filename))
                    except Exception:  # noqa: BLE001
                        pass

    @staticmethod
    def _extract_channel_id(raw):
        match = re.search(r"\d{17,20}", raw)
        return match.group(0) if match else raw

    # ── Message event (spam detection) ────────────────────────────────
    #
    # Only cheap decisions run here on the IPC loop; the actual image fetch,
    # embedding, comparison, and punishment happen on the worker thread.

    def on_message(self, data):
        guild_id = data.get("guild_id")
        author = data.get("author") or {}
        if author.get("bot") or not guild_id:
            return

        # A pending `add` takes priority over spam detection for this message.
        # Consume it here (single-use) on the IPC thread so the check-and-add is
        # atomic and only the requesting user's message is honored.
        add_key = (guild_id, data.get("channel_id"), author.get("id"))
        if add_key in self.pending_add:
            del self.pending_add[add_key]
            self._queue.put({"op": "add", "data": data})
            return

        if not (self.model_loaded and self._guild_settings(guild_id)["enabled"]):
            return
        if any((a.get("content_type") or "").startswith("image/")
               for a in (data.get("attachments") or [])):
            self._queue.put({"op": "check", "data": data})

    def _do_check(self, data):
        guild_id = data.get("guild_id")
        author = data.get("author") or {}
        settings = self._guild_settings(guild_id)
        for att in data.get("attachments") or []:
            if not (att.get("content_type") or "").startswith("image/"):
                continue
            proxy_url = att.get("proxy_url")
            if not proxy_url:
                continue
            try:
                image_bytes = self._fetch_bytes(proxy_url)
                score = self._check_similarity(image_bytes)
            except Exception as e:  # noqa: BLE001
                self.logger.error(f"Error analyzing image: {e}")
                continue
            if score > settings["threshold"]:
                self._execute_punishment(
                    data.get("message_id"),
                    data.get("channel_id"),
                    guild_id,
                    author.get("id"),
                    score,
                    proxy_url,
                )
                return

    def _do_add(self, data):
        if not self.model_loaded:
            self.logger.info("add skipped: CLIP model not loaded yet")
            return
        author = data.get("author") or {}
        if author.get("bot"):
            return
        # Add every image attachment in the message (not just the first).
        image_atts = [
            a for a in (data.get("attachments") or [])
            if (a.get("content_type") or "").startswith("image/")
        ]
        if not image_atts:
            return
        spam_dir = os.path.join(self.data_dir, "spam_images")
        os.makedirs(spam_dir, exist_ok=True)
        added = []
        used_names = set()
        for att in image_atts:
            try:
                image_bytes = self._fetch_bytes(att.get("proxy_url"))
            except Exception as e:  # noqa: BLE001
                self.logger.error(f"Failed to fetch image for add: {e}")
                continue
            base = re.sub(r"[^A-Za-z0-9._-]", "_",
                          att.get("filename", f"spam_{data.get('message_id')}"))
            filename = base
            suffix = 1
            while filename in used_names:
                stem, ext = os.path.splitext(base)
                filename = f"{stem}_{suffix}{ext}"
                suffix += 1
            used_names.add(filename)
            save_path = os.path.join(spam_dir, filename)
            try:
                with open(save_path, "wb") as f:
                    f.write(image_bytes)
            except Exception as e:  # noqa: BLE001
                self.logger.error(f"Failed to save image {filename}: {e}")
                continue
            added.append(filename)
        if not added:
            return
        self._refresh_spam_vectors()
        try:
            self.rest.create_message(
                data.get("channel_id"),
                f"✅ Added {len(added)} image(s): "
                f"{', '.join('`' + f + '`' for f in added)}. "
                f"Total reference images: {len(self.spam_vectors)}",
            )
        except Exception:  # noqa: BLE001
            pass

    # ── Punishment ────────────────────────────────────────────────────

    def _execute_punishment(self, message_id, channel_id, guild_id, author_id, score, proxy_url):
        settings = self._guild_settings(guild_id)
        punishment = settings["punishment"]

        should_delete = True
        if punishment == "none" and not settings["delete_on_none"]:
            should_delete = False

        if should_delete:
            try:
                self.rest.delete_message(channel_id, message_id)
            except Exception:  # noqa: BLE001 - NotFound / Forbidden
                self.logger.warn(f"No permission to delete message in guild {guild_id}")

        action_taken = "None"
        failure = None

        if punishment != "none":
            if self._is_immune(guild_id, author_id):
                action_taken = f"Failed ({punishment}): User Hierarchy/Owner"
            else:
                reason = f"Spam Image Detected (AI Score: {score:.2f})"
                try:
                    if punishment == "kick":
                        self.rest.kick_member(guild_id, author_id, reason=reason)
                        action_taken = "Kicked"
                    elif punishment == "ban":
                        self.rest.ban_member(guild_id, author_id, delete_message_days=1, reason=reason)
                        action_taken = "Banned"
                    elif punishment == "mute":
                        until = datetime.now(timezone.utc) + timedelta(seconds=settings["mute_duration"])
                        self.rest.timeout_member(
                            guild_id, author_id,
                            communication_disabled_until=until.isoformat(),
                        )
                        action_taken = f"Muted ({settings['mute_duration']}s)"
                except Exception as e:  # noqa: BLE001 - REST failure: alert the logs channel + terminal, never the main channel
                    self.logger.error(
                        f"[image_spam_filter] {punishment.upper()} failed for user "
                        f"{author_id} in guild {guild_id}: {e}"
                    )
                    action_taken = f"Failed ({punishment}): {e}"
                    failure = str(e)
        elif should_delete:
            action_taken = "Message Deleted (No Punishment)"

        log_channel_id = settings["log_channel"]
        if failure:
            # Punishment failed: surface it as an error to the logs channel (if
            # configured). Deliberately NOT posted to the main channel where the
            # spam image was detected.
            self._log_failure(channel_id, author_id, punishment, score, proxy_url, failure, log_channel_id)
        elif log_channel_id:
            self._log_detection(channel_id, author_id, action_taken, score, log_channel_id, proxy_url)

    def _log_failure(self, channel_id, author_id, punishment, score, proxy_url, failure, log_channel_id):
        """Post a punishment failure to the action logs channel (if configured).

        Runs on the worker thread. Never posts to the main channel — the failure
        is reported to the configured ``log_channel`` and to the bot's terminal
        logger. If there is no logs channel configured, only the terminal log is
        written.
        """
        if not log_channel_id:
            return
        description = (
            f"**User:** {author_id}\n"
            f"**Punishment:** {punishment.upper()} (FAILED)\n"
            f"**Similarity Score:** `{score:.4f}`\n"
            f"**Channel:** {channel_id}\n"
            f"**Error:** `{failure}`"
        )
        embed = {
            "title": "\u274c Image Spam Punishment Failed",
            "description": description,
            "color": 16711680,  # red
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "image": {"url": proxy_url} if proxy_url else None,
            "footer": {"text": "OpenAI CLIP Analysis (CPU)"},
        }
        try:
            self.rest.create_message(log_channel_id, embed=embed)
        except Exception as e:  # noqa: BLE001
            self.logger.error(f"[image_spam_filter] Failed to send failure alert to {log_channel_id}: {e}")

    def _is_immune(self, guild_id, author_id):
        # Guild owner is always immune.
        if guild_id and author_id:
            try:
                guild = self.rest.get_guild(guild_id)
                if guild and guild.get("owner_id") == author_id:
                    return True
            except Exception:  # noqa: BLE001
                pass
            # Role-hierarchy immunity: author's top role >= bot's top role.
            bot_id = self._bot_id()
            if bot_id and bot_id != author_id:
                try:
                    roles = self.rest.get_guild_roles(guild_id)
                    if roles:
                        pos = {r["id"]: r.get("position", 0) for r in roles}
                        member = self.rest.get_member(guild_id, author_id)
                        member_roles = member.get("roles", []) if member else []
                        target_top = max((pos.get(rid, 0) for rid in member_roles), default=0)
                        bot_member = self.rest.get_member(guild_id, bot_id)
                        bot_roles = bot_member.get("roles", []) if bot_member else []
                        bot_top = max((pos.get(rid, 0) for rid in bot_roles), default=0)
                        if target_top >= bot_top:
                            return True
                except Exception:  # noqa: BLE001
                    pass
        return False

    def _bot_id(self):
        try:
            me = self.rest.get_user("me")
            if me and me.get("id"):
                return me["id"]
        except Exception:  # noqa: BLE001
            pass
        return None

    def _log_detection(self, channel_id, author_id, action_taken, score, log_channel_id, proxy_url):
        description = (
            f"**User:** {author_id}\n"
            f"**Action:** {action_taken}\n"
            f"**Similarity Score:** `{score:.4f}`\n"
            f"**Channel:** {channel_id}"
        )
        embed = {
            "title": "🛡️ Image Spam Detected",
            "description": description,
            "color": 16711680,  # red
            "timestamp": datetime.now(timezone.utc).isoformat(),
            # Remote embed image: the SDK's REST layer can't upload files, so
            # point at the attachment's public CDN URL instead.
            "image": {"url": proxy_url},
            "footer": {"text": "OpenAI CLIP Analysis (CPU)"},
        }
        try:
            self.rest.create_message(log_channel_id, embed=embed)
        except Exception as e:  # noqa: BLE001
            self.logger.error(f"Failed to send log to channel {log_channel_id}: {e}")


module = ImageSpamFilter()
