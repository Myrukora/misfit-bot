"""
Context class for Misfit Python modules.

Provides the interface for interacting with the bot and Discord.
"""

import json
import sys
from typing import Any, Optional

from misfit.rest import RestAPI
from misfit.voice import VoiceContext


class Logger:
    """Logger wrapper that sends log messages via IPC."""

    def __init__(self, ipc):
        self._ipc = ipc

    def debug(self, message: str) -> None:
        """Log a debug message."""
        self._ipc.send({"type": "log", "level": "debug", "message": message})

    def info(self, message: str) -> None:
        """Log an info message."""
        self._ipc.send({"type": "log", "level": "info", "message": message})

    def warn(self, message: str) -> None:
        """Log a warning message."""
        self._ipc.send({"type": "log", "level": "warn", "message": message})

    def error(self, message: str) -> None:
        """Log an error message."""
        self._ipc.send({"type": "log", "level": "error", "message": message})


class Context:
    """
    Command execution context.

    Provides methods for responding to commands and accessing bot information.
    """

    def __init__(self, ipc, channel_id: str, guild_id: str, author_id: str,
                 args: list, is_slash: bool, req_id: str = ""):
        self._ipc = ipc
        self.channel_id = channel_id
        self.guild_id = guild_id
        self.author_id = author_id
        self.args = args
        self.is_slash = is_slash
        self.req_id = req_id  # non-empty when invoked from the web dashboard

    def respond(self, title: str, description: str = "") -> None:
        """
        Send an embed response.

        Args:
            title: Embed title
            description: Embed description
        """
        msg = {
            "type": "respond",
            "channel_id": self.channel_id,
            "title": title,
            "description": description
        }
        if self.req_id:
            msg["req_id"] = self.req_id
        self._ipc.send(msg)

    def reply_text(self, text: str) -> None:
        """
        Send a plain text response.

        Args:
            text: Text to send
        """
        msg = {
            "type": "reply_text",
            "channel_id": self.channel_id,
            "text": text
        }
        if self.req_id:
            msg["req_id"] = self.req_id
        self._ipc.send(msg)


class BotContext:
    """
    Bot information context.

    Provides access to bot information, utilities, the Discord REST API,
    and external HTTP requests.
    """

    def __init__(self, ipc, bot_info: dict):
        self._ipc = ipc
        self._bot_info = bot_info
        self.logger = Logger(ipc)
        self.rest = RestAPI(ipc)
        self.voice = VoiceContext(ipc)

    @property
    def bot_name(self) -> str:
        """Get the bot's name."""
        return self._bot_info.get("bot_name", "")

    @property
    def owner_id(self) -> str:
        """Get the bot owner's user ID."""
        return self._bot_info.get("owner_id", "")

    @property
    def prefix(self) -> str:
        """Get the bot's command prefix."""
        return self._bot_info.get("prefix", "")

    @property
    def version(self) -> str:
        """Get the bot's version."""
        return self._bot_info.get("version", "")

    @property
    def data_dir(self) -> str:
        """Get the module's data directory."""
        return self._bot_info.get("data_dir", "")

    def http_request(self, method: str, url: str,
                     headers: Optional[dict] = None,
                     body: str = "",
                     timeout: float = 30.0) -> dict:
        """
        Make an HTTP request to any external API.

        This is proxied through the Go bot, so CORS is not an issue.
        Useful for calling external services like OpenAI, CLIP endpoints, etc.

        Args:
            method: HTTP method (GET, POST, PUT, DELETE, etc.)
            url: Full URL to request
            headers: Optional HTTP headers as dict
            body: Optional request body string
            timeout: Timeout in seconds (default 30)

        Returns:
            Response dict with 'status' (int) and 'body' (str) fields

        Raises:
            IPCError: If the request fails
        """
        result = self._ipc.call("http_request", {
            "method": method,
            "url": url,
            "headers": headers or {},
            "body": body,
        }, timeout=timeout)
        return {
            "status": result.get("status", 0),
            "body": result.get("body", ""),
        }
