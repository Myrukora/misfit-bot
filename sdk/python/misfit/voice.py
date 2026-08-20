"""
VoiceContext class for Misfit Python modules.

Provides the interface for controlling voice channel connections
and audio playback through the Go bot's VoiceManager.
"""

from typing import Optional


class VoiceContext:
    """
    Voice channel control interface.

    Provides methods for joining/leaving voice channels, playing audio,
    and controlling playback. All operations are proxied through Go.
    """

    def __init__(self, ipc):
        self._ipc = ipc

    def join(self, guild_id: str, channel_id: str) -> None:
        """
        Join a voice channel in a guild.

        Args:
            guild_id: The guild ID to join
            channel_id: The voice channel ID to join

        Raises:
            IPCError: If the voice manager is unavailable or join fails
        """
        self._ipc.call("voice", {
            "action": "join",
            "params": {"guild_id": guild_id, "channel_id": channel_id},
        })

    def leave(self, guild_id: str) -> None:
        """
        Leave the voice channel in a guild.

        Args:
            guild_id: The guild ID to leave

        Raises:
            IPCError: If not connected or leave fails
        """
        self._ipc.call("voice", {
            "action": "leave",
            "params": {"guild_id": guild_id},
        })

    def play(self, guild_id: str, source: str) -> None:
        """
        Play audio from a source URL or file path in a voice channel.

        The bot must already be connected to a voice channel in the guild.
        Audio is decoded via FFmpeg and streamed through the Go bot.

        Args:
            guild_id: The guild ID to play in
            source: URL or file path to the audio source

        Raises:
            IPCError: If playback fails
        """
        self._ipc.call("voice", {
            "action": "play",
            "params": {"guild_id": guild_id, "source": source},
        })

    def stop(self, guild_id: str) -> None:
        """
        Stop current audio playback in a guild.

        The bot stays connected to the voice channel.

        Args:
            guild_id: The guild ID to stop playback in

        Raises:
            IPCError: If no playback is active
        """
        self._ipc.call("voice", {
            "action": "stop",
            "params": {"guild_id": guild_id},
        })

    def pause(self, guild_id: str) -> None:
        """
        Pause current audio playback.

        Resume with resume().

        Args:
            guild_id: The guild ID to pause in

        Raises:
            IPCError: If no active playback
        """
        self._ipc.call("voice", {
            "action": "pause",
            "params": {"guild_id": guild_id},
        })

    def resume(self, guild_id: str) -> None:
        """
        Resume paused audio playback.

        Args:
            guild_id: The guild ID to resume in

        Raises:
            IPCError: If no paused playback
        """
        self._ipc.call("voice", {
            "action": "resume",
            "params": {"guild_id": guild_id},
        })

    def set_volume(self, guild_id: str, volume: float) -> None:
        """
        Set playback volume (0.0 to 2.0). 1.0 is normal.

        Args:
            guild_id: The guild ID to adjust
            volume: Volume level (0.0 to 2.0)

        Raises:
            IPCError: If volume is out of range or no active connection
        """
        self._ipc.call("voice", {
            "action": "set_volume",
            "params": {"guild_id": guild_id, "volume": volume},
        })

    def set_mute(self, guild_id: str, mute: bool = True) -> None:
        """
        Toggle self-mute on the voice connection.

        Args:
            guild_id: The guild ID
            mute: True to mute, False to unmute

        Raises:
            IPCError: If not connected to voice
        """
        self._ipc.call("voice", {
            "action": "set_mute",
            "params": {"guild_id": guild_id, "mute": mute},
        })

    def set_deafen(self, guild_id: str, deafen: bool = True) -> None:
        """
        Toggle self-deafen on the voice connection.

        Args:
            guild_id: The guild ID
            deafen: True to deafen, False to undeafen

        Raises:
            IPCError: If not connected to voice
        """
        self._ipc.call("voice", {
            "action": "set_deafen",
            "params": {"guild_id": guild_id, "deafen": deafen},
        })

    def is_connected(self, guild_id: str) -> bool:
        """
        Check if the bot is connected to a voice channel in a guild.

        Args:
            guild_id: The guild ID

        Returns:
            True if connected to a voice channel
        """
        result = self._ipc.call("voice", {
            "action": "is_connected",
            "params": {"guild_id": guild_id},
        })
        return result.get("connected", False)

    def is_playing(self, guild_id: str) -> bool:
        """
        Check if audio is currently playing in a guild.

        Args:
            guild_id: The guild ID

        Returns:
            True if audio is playing
        """
        result = self._ipc.call("voice", {
            "action": "is_playing",
            "params": {"guild_id": guild_id},
        })
        return result.get("playing", False)
