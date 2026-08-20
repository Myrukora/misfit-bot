"""
Misfit Python SDK

This package provides the base classes and utilities for writing Python modules
for the Misfit Discord bot.

Usage:
    from misfit import Module, Command

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

from misfit.module import Module
from misfit.commands import Command, SlashCommand
from misfit.context import Context, BotContext
from misfit.rest import RestAPI
from misfit.voice import VoiceContext

__all__ = ['Module', 'Command', 'SlashCommand', 'Context', 'BotContext', 'RestAPI', 'VoiceContext']
