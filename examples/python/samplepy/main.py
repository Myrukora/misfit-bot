from misfit import Module, Command


class SampleModule(Module):
    name = "samplepy"
    version = "1.0.0"
    description = "Sample Python module for the dashboard runner"
    author = "dev"

    def on_load(self, ctx) -> None:
        pass

    def on_unload(self) -> None:
        pass

    def commands(self):
        return [
            Command(
                name="pyhello",
                description="Say hello from Python",
                usage="pyhello <name>",
                category="sample",
                execute=self.hello,
            ),
            Command(
                name="pydemo",
                description="Demo command from Python",
                usage="pydemo",
                category="sample",
                execute=self.demo,
            ),
        ]

    def slash_commands(self):
        return []

    def event_handlers(self):
        return {}

    def hello(self, ctx):
        name = ctx.args[0] if ctx.args else "world"
        ctx.reply_text(f"Hello, {name}!")

    def demo(self, ctx):
        ctx.respond("Python Demo", "This command was executed via IPC.")


module = SampleModule()
