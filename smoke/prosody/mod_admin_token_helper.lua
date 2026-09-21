-- Issues a prosody:admin bearer token for admin@<host> when the host loads
-- and writes it where the smoke test's healthcheck and client can read it.
local tokenauth = module:depends("tokenauth")

module:hook("module-loaded", function()
    local jid_admin = "admin@" .. module.host
    local grant = tokenauth.create_grant(jid_admin, jid_admin, nil, {role = "prosody:admin"})
    if not grant then
        module:log("error", "could not create admin grant")
        return
    end
    local token = tokenauth.create_token(jid_admin, grant, "prosody:admin", 86400, nil, nil)
    if not token then
        module:log("error", "could not create admin token")
        return
    end
    local f = assert(io.open("/var/lib/prosody/admin-token.txt", "w"))
    f:write(token)
    f:close()
    module:log("info", "admin token written")
end)
