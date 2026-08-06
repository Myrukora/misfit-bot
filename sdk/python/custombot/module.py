"""
Base Module class for CustomBot Python modules.
"""

from abc import ABC, abstractmethod
from typing import List, Optional

from custombot.commands import Command, SlashCommand


class Module(ABC):
    """
    Base class for all Python modules.

    Subclasses must define:
        name (str): Module name
        version (str): Module version
        description (str): Module description
        author (str): Module author

    Subclasses must implement:
        on_load(ctx): Called when module is loaded
        on_unload(): Called when module is unloaded
        commands(): Returns list of Command objects
        slash_commands(): Returns list of SlashCommand objects
        event_handlers(): Returns dict of event name -> handler function
    """

    name: str = ""
    version: str = "1.0.0"
    description: str = ""
    author: str = ""

    @abstractmethod
    def on_load(self, ctx) -> None:
        """Called when the module is loaded."""
        pass

    @abstractmethod
    def on_unload(self) -> None:
        """Called when the module is unloaded."""
        pass

    @abstractmethod
    def commands(self) -> List[Command]:
        """Returns a list of prefix commands."""
        raise NotImplementedError

    @abstractmethod
    def slash_commands(self) -> List[SlashCommand]:
        """Returns a list of slash commands."""
        raise NotImplementedError

    @abstractmethod
    def event_handlers(self) -> dict:
        """Returns a dict of event name -> handler function."""
        raise NotImplementedError

    def __repr__(self) -> str:
        return f"<Module {self.name} v{self.version}>"
