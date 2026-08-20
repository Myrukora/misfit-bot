from misfit import Module, Command, SlashCommand


class HelloModule(Module):
    name = "hello_py"
    version = "1.0.0"
    description = "A simple example Python module"
    author = "Misfit"

    def on_load(self, ctx):
        ctx.logger.info("Hello Python module loaded!")

    def on_unload(self):
        pass

    def commands(self):
        return [
            Command(
                name="hello",
                description="Say hello from the Python example module.",
                usage="hello",
                category="fun",
                execute=self.hello_command,
            ),
            Command(
                name="pyinfo",
                description="Show info about the Python example module.",
                usage="pyinfo",
                category="info",
                execute=self.pyinfo_command,
            ),
        ]

    def slash_commands(self):
        return [
            SlashCommand(
                name="hello",
                description="Say hello from the Python example module.",
                execute=self.hello_command,
            ),
        ]

    def event_handlers(self):
        return {}

    def hello_command(self, ctx):
        ctx.respond("Hello!", "Hello from Python! This module runs via IPC.")

    def pyinfo_command(self, ctx):
        ctx.respond(
            "Python Module Info",
            "This is a Python module running inside the Go bot via subprocess IPC.",
        )


module = HelloModule()