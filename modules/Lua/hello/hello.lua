-- modules/hello.lua
-- A simple Lua module example for the Discord bot

M = {}

M.name = "hello"
M.version = "1.0.0"
M.description = "A simple hello module written in Lua"
M.author = "sam"

function M.on_load(M, name)
    ctx.log("Hello Lua module loaded!")
end

function M.on_unload()
    -- cleanup if needed
end

function M.commands()
    return {
        {
            name = "hello",
            description = "Say hello from the Lua example module.",
            usage = "hello",
            category = "fun",
            execute = function(M)
                ctx.respond("Hello from Lua!", "This command was written in Lua!")
            end
        },
        {
            name = "luainfo",
            description = "Show info about the Lua example module.",
            usage = "luainfo",
            category = "fun",
            execute = function(M)
                local info = "Lua Module: " .. M.name .. "\n"
                info = info .. "Version: " .. M.version .. "\n"
                info = info .. "Description: " .. M.description
                ctx.respond("Lua Module Info", info)
            end
        }
    }
end

function M.slash_commands()
    return {
        {
            name = "hello-lua",
            description = "Say hello from the Lua example module.",
            category = "fun",
            execute = function(M)
                ctx.respond("Hello from Lua!", "This slash command was written in Lua!")
            end
        }
    }
end
