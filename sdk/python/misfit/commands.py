"""
Command classes for Misfit Python modules.
"""

from dataclasses import dataclass
from typing import Callable, Optional


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
