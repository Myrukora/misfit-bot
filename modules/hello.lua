-- modules/hello.lua
-- A simple Lua module example for the Discord bot

local M = {}

M.name = "hello"
M.version = "1.0.0"
M.description = "A simple hello module written in Lua"
M.author = "sam"

function M:on_load(ctx)
    ctx.log("Hello Lua module loaded!")
end

function M:on_unload()
    -- cleanup if needed
end

function M:commands()
    return {
        {
            name = "hello",
            description = "Says hello from Lua",
            usage = "hello",
            category = "fun",
            execute = function(ctx)
                ctx.respond("Hello from Lua!", "This command was written in Lua!")
            end
        },
        {
            name = "luainfo",
            description = "Shows Lua module info",
            usage = "luainfo",
            category = "fun",
            execute = function(ctx)
                local info = "Lua Module: " .. M.name .. "\n"
                info = info .. "Version: " .. M.version .. "\n"
                info = info .. "Description: " .. M.description
                ctx.respond("Lua Module Info", info)
            end
        }
    }
end

function M:slash_commands()
    return {
        {
            name = "hello-lua",
            description = "Says hello from Lua",
            category = "fun",
            execute = function(ctx)
                ctx.respond("Hello from Lua!", "This slash command was written in Lua!")
            end
        }
    }
end

return M
