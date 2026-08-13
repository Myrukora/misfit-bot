#!/usr/bin/env python3
"""
Runner script for CustomBot Python modules.

Launched by the Go bot to run a Python module. Sets up IPC communication,
imports the user's module, and dispatches messages between bot and module.

Usage: python3 runner.py <path_to_main.py>
"""

import importlib.util
import os
import sys
import traceback

# runner.py lives at sdk/python/custombot/runner.py
# To import custombot, we need sdk/python in sys.path
_sdk_python = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _sdk_python not in sys.path:
    sys.path.insert(0, _sdk_python)

from custombot.ipc import IPC
from custombot.context import BotContext, Context


def load_user_module(main_path):
    """Load the user's module from main.py and return the module instance."""
    module_dir = os.path.dirname(os.path.abspath(main_path))
    if module_dir not in sys.path:
        sys.path.insert(0, module_dir)

    spec = importlib.util.spec_from_file_location("user_module", main_path)
    if spec is None or spec.loader is None:
        raise ImportError(f"Cannot load module from {main_path}")

    user_mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(user_mod)

    if not hasattr(user_mod, "module"):
        raise AttributeError(
            "main.py must define a `module` global variable (instance of Module)"
        )

    return user_mod.module


def command_to_dict(cmd):
    """Convert a Command object to a dict for the ready message."""
    return {
        "name": cmd.name,
        "description": cmd.description,
        "usage": cmd.usage,
        "category": cmd.category,
        "owner_only": cmd.owner_only,
        "aliases": list(cmd.aliases) if cmd.aliases else [],
    }


def slash_command_to_dict(cmd):
    """Convert a SlashCommand object to a dict for the ready message."""
    return {
        "name": cmd.name,
        "description": cmd.description,
        "category": cmd.category,
        "owner_only": cmd.owner_only,
    }


def field_to_dict(f):
    """Convert a dashboard field dict (or object) to the wire schema entry."""
    d = dict(f) if isinstance(f, dict) else {
        k: getattr(f, k) for k in (
            "key", "label", "help", "type", "scope", "guild_scoped",
            "placeholder", "options", "min", "max", "step",
        ) if hasattr(f, k)
    }
    # Normalize: scope/guild_scoped must agree — the dashboard uses
    # GuildScoped to select the config scope, so a guild field that omits
    # guild_scoped must NOT silently default to global.
    scope = d.get("scope")
    if scope is None:
        scope = "guild" if d.get("guild_scoped", False) else "global"
    if scope not in ("global", "guild"):
        raise ValueError(f"invalid dashboard field scope: {scope!r}")
    if "guild_scoped" in d and bool(d["guild_scoped"]) != (scope == "guild"):
        raise ValueError("scope and guild_scoped disagree")
    d["scope"] = scope
    d["guild_scoped"] = scope == "guild"
    d["options"] = list(d.get("options") or [])
    for k in ("min", "max", "step"):
        v = d.get(k)
        d[k] = float(v) if v is not None else None
    return d


def load_dashboard_integration(main_path):
    """Load the optional dashboard.py next to main.py.

    Returns (dash_mod, web_schema): the imported dashboard module (with
    web_get_config/web_set_config callables) and the normalized schema list,
    or (None, []) when the file does not exist.
    """
    module_dir = os.path.dirname(os.path.abspath(main_path))
    dash_path = os.path.join(module_dir, "dashboard.py")
    if not os.path.isfile(dash_path):
        return None, []

    if module_dir not in sys.path:
        sys.path.insert(0, module_dir)
    spec = importlib.util.spec_from_file_location("user_dashboard", dash_path)
    if spec is None or spec.loader is None:
        raise ImportError(f"Cannot load dashboard integration from {dash_path}")
    dash_mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(dash_mod)

    if not hasattr(dash_mod, "web_schema"):
        raise AttributeError(
            "dashboard.py must define `web_schema` (list of field dicts)"
        )
    for fn in ("web_get_config", "web_set_config"):
        if not hasattr(dash_mod, fn) or not callable(getattr(dash_mod, fn)):
            raise AttributeError(f"dashboard.py must define `{fn}`")

    schema = []
    for f in dash_mod.web_schema:
        d = field_to_dict(f)
        if not d.get("key") or not d.get("type"):
            raise AttributeError(
                "each web_schema entry needs 'key' and 'type'"
            )
        schema.append(d)
    return dash_mod, schema


def main():
    if len(sys.argv) < 2:
        print("Usage: runner.py <path_to_main.py>", file=sys.stderr)
        sys.exit(1)

    main_path = sys.argv[1]
    if not os.path.isfile(main_path):
        print(f"Module file not found: {main_path}", file=sys.stderr)
        sys.exit(1)

    # Load the user's module
    try:
        module_instance = load_user_module(main_path)
        dash_mod, web_schema = load_dashboard_integration(main_path)
        has_web_config = dash_mod is not None
    except Exception as e:
        print(f"Failed to load module: {e}", file=sys.stderr)
        traceback.print_exc(file=sys.stderr)
        sys.exit(1)

    # Set up IPC
    ipc = IPC()

    # Collect commands and event handlers
    commands_list = module_instance.commands()
    slash_commands_list = module_instance.slash_commands()
    event_handlers = module_instance.event_handlers()

    # Send ready message with module metadata
    ipc.send({
        "type": "ready",
        "name": module_instance.name,
        "version": module_instance.version,
        "description": module_instance.description,
        "author": module_instance.author,
        "commands": [command_to_dict(c) for c in commands_list],
        "slash_commands": [slash_command_to_dict(c) for c in slash_commands_list],
        "event_handlers": list(event_handlers.keys()) if event_handlers else [],
        "has_web_config": has_web_config,
        "web_schema": web_schema,
    })

    # Build command lookup tables
    cmd_lookup = {c.name: c for c in commands_list}
    slash_lookup = {c.name: c for c in slash_commands_list}

    # Bot context data (populated on init)
    bot_ctx_data = {}

    def handle_init(message):
        ctx_data = message.get("context", {})
        bot_ctx_data.update(ctx_data)
        bot_context = BotContext(ipc, bot_ctx_data)
        try:
            module_instance.on_load(bot_context)
        except Exception as e:
            ipc.send({"type": "error", "message": f"on_load failed: {e}"})
            traceback.print_exc(file=sys.stderr)

    def handle_command(message):
        name = message.get("name", "")
        args = message.get("args", [])
        channel_id = message.get("channel_id", "")
        guild_id = message.get("guild_id", "")
        author_id = message.get("author_id", "")
        is_slash = message.get("is_slash", False)
        # Dashboard-sourced invocations carry source + req_id; the context then
        # echoes req_id in every reply so Go can route it back to the HTTP
        # caller instead of Discord.
        req_id = message.get("req_id", "")

        def err(msg):
            err_msg = {"type": "error", "message": msg}
            if req_id:
                err_msg["req_id"] = req_id
            return err_msg

        cmd = cmd_lookup.get(name) or slash_lookup.get(name)
        if cmd is None:
            ipc.send(err(f"Unknown command: {name}"))
            return

        ctx = Context(ipc, channel_id, guild_id, author_id, args, is_slash, req_id=req_id)
        try:
            cmd.execute(ctx)
        except Exception as e:
            ipc.send(err(f"Command '{name}' failed: {e}"))
            traceback.print_exc(file=sys.stderr)

    def handle_event(message):
        name = message.get("name", "")
        data = message.get("data", {})
        handler = event_handlers.get(name) if event_handlers else None
        if handler is None:
            return
        try:
            handler(data)
        except Exception as e:
            ipc.send({"type": "error", "message": f"Event '{name}' failed: {e}"})
            traceback.print_exc(file=sys.stderr)

    def handle_shutdown(message):
        try:
            module_instance.on_unload()
        except Exception:
            traceback.print_exc(file=sys.stderr)
        ipc.stop()
        sys.exit(0)

    def handle_web_get_config(message):
        """Dashboard settings read: web_get_config {req_id, guild_id}."""
        req_id = message.get("req_id", "")
        try:
            values = web_get_config_fn(message.get("guild_id", ""))
            ipc.send({"type": "web_config_response", "req_id": req_id,
                      "values": values or {}})
        except Exception as e:
            ipc.send({"type": "web_config_response", "req_id": req_id,
                      "error": str(e)})

    def handle_web_set_config(message):
        """Dashboard settings write: web_set_config {req_id, guild_id, key, value}."""
        req_id = message.get("req_id", "")
        try:
            web_set_config_fn(message.get("guild_id", ""),
                              message.get("key", ""),
                              message.get("value", ""))
            ipc.send({"type": "web_config_response", "req_id": req_id, "ok": True})
        except Exception as e:
            ipc.send({"type": "web_config_response", "req_id": req_id,
                      "error": str(e)})

    ipc.register_handler("init", handle_init)
    ipc.register_handler("command", handle_command)
    ipc.register_handler("event", handle_event)
    ipc.register_handler("shutdown", handle_shutdown)
    if has_web_config:
        web_get_config_fn = dash_mod.web_get_config
        web_set_config_fn = dash_mod.web_set_config
        ipc.register_handler("web_get_config", handle_web_get_config)
        ipc.register_handler("web_set_config", handle_web_set_config)

    # Start the message loop (blocks until shutdown)
    ipc.start()
    ipc.wait_for_shutdown()


if __name__ == "__main__":
    main()