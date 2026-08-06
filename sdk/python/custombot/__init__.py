"""
CustomBot Python SDK

This package provides the base classes and utilities for writing Python modules
for the CustomBot Discord bot.

Usage:
    from custombot import Module, Command

    class MyModule(Module):
        name = "my_module"
        version = "1.0.0"
        description = "My awesome module"
        author = "Your Name"

        def on_load(self, ctx):
            ctx.logger.info("Module loaded!")

        def on_unload(self):
            pass

        def commands(self):
            return [
                Command(
                    name="hello",
                    description="Say hello",
                    execute=self.cmd_hello
                )
            ]

        def cmd_hello(self, ctx):
            ctx.respond("Hello!", "Hello from Python!")

    module = MyModule()
"""

from custombot.module import Module
from custombot.commands import Command, SlashCommand
from custombot.context import Context, BotContext
from custombot.rest import RestAPI
from custombot.voice import VoiceContext

__all__ = ['Module', 'Command', 'SlashCommand', 'Context', 'BotContext', 'RestAPI', 'VoiceContext']
