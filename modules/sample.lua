M = {}
M.name = "sample"
M.version = "1.0.0"
M.description = "Sample Lua module for the dashboard runner"
M.author = "dev"

function M.on_load(ctx)
end

function M.on_unload(M)
end

function M.commands(M)
  return {
    {
      name = "hello",
      description = "Say hello",
      usage = "hello <name>",
      category = "sample",
      execute = function(M)
        local name = ctx.args[1] or "world"
        ctx.reply_text("Hello, " .. name .. "!")
      end,
    },
    {
      name = "sampleinfo",
      description = "Show sample module info",
      usage = "sampleinfo",
      category = "sample",
      execute = function(M)
        local args = ""
        for i = 1, #ctx.args do
          args = args .. " " .. ctx.args[i]
        end
        ctx.respond("Sample Module", "Lua command executed. Args:" .. args)
      end,
    },
  }
end
