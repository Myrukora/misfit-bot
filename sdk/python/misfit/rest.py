"""
REST API wrapper for Misfit Python modules.

Provides access to the Discord REST API through the Go bot's HTTP proxy.
This keeps the bot token secure in the Go process — Python never sees it.

Usage:
    bot.rest.delete_message(channel_id, message_id)
    bot.rest.ban_member(guild_id, user_id, reason="spam")
    member = bot.rest.get_member(guild_id, user_id)
"""

from typing import Any, Dict, List, Optional


class RestAPI:
    """
    Discord REST API wrapper.

    All calls are proxied through the Go bot which holds the bot token.
    """

    def __init__(self, ipc):
        self._ipc = ipc

    def request(self, method: str, endpoint: str, **kwargs) -> Any:
        """
        Make a generic Discord API request.

        Args:
            method: HTTP method (GET, POST, DELETE, PATCH, PUT)
            endpoint: API endpoint path (e.g., /channels/{id}/messages/{id})
            **kwargs: May include 'json' for request body

        Returns:
            Parsed response data, or None for empty responses

        Raises:
            IPCError: If the request fails
        """
        body = kwargs.get("json")
        result = self._ipc.call("api_request", {
            "method": method,
            "endpoint": endpoint,
            "body": body,
        })
        return result.get("data")

    # ── Message operations ──────────────────────────────────

    def get_message(self, channel_id: str, message_id: str) -> dict:
        """Get a message by ID."""
        return self.request("GET", f"/channels/{channel_id}/messages/{message_id}")

    def delete_message(self, channel_id: str, message_id: str) -> None:
        """Delete a message."""
        self.request("DELETE", f"/channels/{channel_id}/messages/{message_id}")

    def create_message(self, channel_id: str, content: str = "",
                       embed: Optional[dict] = None) -> dict:
        """Create a message in a channel."""
        body = {}
        if content:
            body["content"] = content
        if embed:
            body["embeds"] = [embed]
        return self.request("POST", f"/channels/{channel_id}/messages", json=body)

    def edit_message(self, channel_id: str, message_id: str,
                     content: str = "", embed: Optional[dict] = None) -> dict:
        """Edit an existing message."""
        body = {}
        if content:
            body["content"] = content
        if embed:
            body["embeds"] = [embed]
        return self.request("PATCH", f"/channels/{channel_id}/messages/{message_id}", json=body)

    def get_channel_messages(self, channel_id: str, limit: int = 50,
                             before: Optional[str] = None,
                             after: Optional[str] = None) -> List[dict]:
        """Get messages from a channel."""
        params = f"?limit={limit}"
        if before:
            params += f"&before={before}"
        if after:
            params += f"&after={after}"
        return self.request("GET", f"/channels/{channel_id}/messages{params}")

    def add_reaction(self, channel_id: str, message_id: str, emoji: str) -> None:
        """Add a reaction to a message."""
        self.request("PUT", f"/channels/{channel_id}/messages/{message_id}/reactions/{emoji}/@me")

    def remove_reaction(self, channel_id: str, message_id: str,
                        emoji: str, user_id: str = "@me") -> None:
        """Remove a reaction from a message."""
        self.request("DELETE", f"/channels/{channel_id}/messages/{message_id}/reactions/{emoji}/{user_id}")

    def pin_message(self, channel_id: str, message_id: str) -> None:
        """Pin a message in a channel."""
        self.request("PUT", f"/channels/{channel_id}/pins/{message_id}")

    def unpin_message(self, channel_id: str, message_id: str) -> None:
        """Unpin a message in a channel."""
        self.request("DELETE", f"/channels/{channel_id}/pins/{message_id}")

    # ── Channel operations ──────────────────────────────────

    def get_channel(self, channel_id: str) -> dict:
        """Get channel information."""
        return self.request("GET", f"/channels/{channel_id}")

    def delete_channel(self, channel_id: str) -> None:
        """Delete a channel."""
        self.request("DELETE", f"/channels/{channel_id}")

    def create_channel(self, guild_id: str, name: str, channel_type: int = 0,
                       **kwargs) -> dict:
        """Create a channel in a guild."""
        body = {"name": name, "type": channel_type, **kwargs}
        return self.request("POST", f"/guilds/{guild_id}/channels", json=body)

    # ── Member operations ───────────────────────────────────

    def get_member(self, guild_id: str, user_id: str) -> dict:
        """Get guild member information."""
        return self.request("GET", f"/guilds/{guild_id}/members/{user_id}")

    def kick_member(self, guild_id: str, user_id: str,
                    reason: str = "") -> None:
        """Kick a member from the guild."""
        self.request("DELETE", f"/guilds/{guild_id}/members/{user_id}")

    def ban_member(self, guild_id: str, user_id: str,
                   delete_message_days: int = 0, reason: str = "") -> None:
        """Ban a member from the guild."""
        body = {}
        if delete_message_days:
            body["delete_message_days"] = delete_message_days
        if reason:
            body["reason"] = reason
        self.request("PUT", f"/guilds/{guild_id}/bans/{user_id}", json=body)

    def unban_member(self, guild_id: str, user_id: str) -> None:
        """Unban a member from the guild."""
        self.request("DELETE", f"/guilds/{guild_id}/bans/{user_id}")

    def timeout_member(self, guild_id: str, user_id: str,
                       communication_disabled_until: str) -> None:
        """
        Timeout a guild member.

        Args:
            guild_id: The guild ID
            user_id: The user ID
            communication_disabled_until: ISO8601 timestamp (e.g.,
                "2026-07-24T13:00:00.000Z") or datetime string.
                Set to None to remove timeout.
        """
        body = {}
        if communication_disabled_until:
            body["communication_disabled_until"] = communication_disabled_until
        else:
            body["communication_disabled_until"] = None
        self.request("PATCH", f"/guilds/{guild_id}/members/{user_id}", json=body)

    def add_member_role(self, guild_id: str, user_id: str, role_id: str) -> None:
        """Add a role to a guild member."""
        self.request("PUT", f"/guilds/{guild_id}/members/{user_id}/roles/{role_id}")

    def remove_member_role(self, guild_id: str, user_id: str, role_id: str) -> None:
        """Remove a role from a guild member."""
        self.request("DELETE", f"/guilds/{guild_id}/members/{user_id}/roles/{role_id}")

    # ── Guild operations ────────────────────────────────────

    def get_guild(self, guild_id: str) -> dict:
        """Get guild information."""
        return self.request("GET", f"/guilds/{guild_id}")

    def get_guild_channels(self, guild_id: str) -> List[dict]:
        """Get all channels in a guild."""
        return self.request("GET", f"/guilds/{guild_id}/channels")

    def get_guild_roles(self, guild_id: str) -> List[dict]:
        """Get all roles in a guild."""
        return self.request("GET", f"/guilds/{guild_id}/roles")

    def get_guild_bans(self, guild_id: str) -> List[dict]:
        """Get all bans in a guild."""
        return self.request("GET", f"/guilds/{guild_id}/bans")

    def get_guild_members(self, guild_id: str, limit: int = 1000,
                          after: Optional[str] = None) -> List[dict]:
        """Get members of a guild."""
        params = f"?limit={limit}"
        if after:
            params += f"&after={after}"
        return self.request("GET", f"/guilds/{guild_id}/members{params}")

    # ── User operations ─────────────────────────────────────

    def get_user(self, user_id: str) -> dict:
        """Get user information."""
        return self.request("GET", f"/users/{user_id}")

    # ── Role operations ─────────────────────────────────────

    def create_role(self, guild_id: str, name: str,
                    permissions: Optional[str] = None,
                    color: Optional[int] = None,
                    hoist: bool = False,
                    mentionable: bool = False) -> dict:
        """Create a role in a guild."""
        body = {"name": name, "hoist": hoist, "mentionable": mentionable}
        if permissions is not None:
            body["permissions"] = permissions
        if color is not None:
            body["color"] = color
        return self.request("POST", f"/guilds/{guild_id}/roles", json=body)

    def delete_role(self, guild_id: str, role_id: str) -> None:
        """Delete a role."""
        self.request("DELETE", f"/guilds/{guild_id}/roles/{role_id}")

    # ── Emoji operations ────────────────────────────────────

    def get_guild_emojis(self, guild_id: str) -> List[dict]:
        """Get all emojis for a guild."""
        return self.request("GET", f"/guilds/{guild_id}/emojis")

    # ── Webhook operations ──────────────────────────────────

    def create_webhook(self, channel_id: str, name: str,
                       avatar: Optional[str] = None) -> dict:
        """Create a webhook in a channel."""
        body = {"name": name}
        if avatar:
            body["avatar"] = avatar
        return self.request("POST", f"/channels/{channel_id}/webhooks", json=body)
