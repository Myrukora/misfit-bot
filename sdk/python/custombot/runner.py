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

    ipc.register_handler("init", handle_init)
    ipc.register_handler("command", handle_command)
    ipc.register_handler("event", handle_event)
    ipc.register_handler("shutdown", handle_shutdown)

    # Start the message loop (blocks until shutdown)
    ipc.start()
    ipc.wait_for_shutdown()


if __name__ == "__main__":
    main()