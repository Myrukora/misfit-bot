"""
Command classes for Misfit Python modules.
"""

from dataclasses import dataclass, field
from typing import Callable, List, Optional


@dataclass
class Command:
    """
    Represents a prefix command.

    Attributes:
        name: Command name (used to invoke with prefix)
        description: Short description of what the command does
        usage: Usage example (e.g., "hello <name>")
        category: Command category (e.g., "fun", "moderation")
        owner_only: If True, only bot owner can use this command
        required_perm: Discord permission required to use this command
        aliases: Alternative names for this command
        execute: Function to call when command is invoked
    """

    name: str
    description: str = ""
    usage: str = ""
    category: str = "general"
    owner_only: bool = False
    required_perm: int = 0
    aliases: List[str] = field(default_factory=list)
    execute: Optional[Callable] = None

    def __post_init__(self):
        if self.execute is None:
            raise ValueError("Command must have an execute function")


@dataclass
class SlashCommand:
    """
    Represents a slash command.

    Attributes:
        name: Command name (used in Discord slash command menu)
        description: Short description of what the command does
        category: Command category (e.g., "fun", "moderation")
        owner_only: If True, only bot owner can use this command
        required_perm: Discord permission required to use this command
        execute: Function to call when command is invoked
    """

    name: str
    description: str = ""
    category: str = "general"
    owner_only: bool = False
    required_perm: int = 0
    execute: Optional[Callable] = None

    def __post_init__(self):
        if self.execute is None:
            raise ValueError("SlashCommand must have an execute function")
