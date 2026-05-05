-- mod_admin_sessions: REST endpoints for live c2s session management.
--
-- Prosody 13's mod_http_admin_api (community) does not expose live session
-- listing or session disconnection. This module fills that gap with the
-- three endpoints XMPanel's prosody adapter expects.
--
-- Auth: same pattern as mod_http_admin_api — Bearer token issued by
--       mod_tokenauth, with role-gated permission `:access-admin-api`.
-- Mount: VirtualHost-scoped. Sessions outside the loaded host are NOT
--        listed even though prosody.full_sessions is process-global.
--
-- Endpoints:
--   GET    /admin_sessions
--   DELETE /admin_sessions/{full_jid}
--   POST   /admin_sessions/disconnect/{username}
--
-- Drop-in install:
--   1. Place this file at /etc/prosody/modules/mod_admin_sessions.lua
--   2. Add "admin_sessions" to the VirtualHost's modules_enabled list.
--   3. systemctl restart prosody.

local jid_lib = require "util.jid"
local jid_join = jid_lib.join
local jid_bare = jid_lib.bare
local json = require "util.json"

local tokens = module:depends("tokenauth")

module:depends("http")

-- Permission gate: only roles allowed to use the admin API can use this.
-- mod_http_admin_api declares the same permission with the same default,
-- so granting prosody:admin once covers both modules.
module:default_permission("prosody:admin", ":access-admin-api")

local www_authenticate_header = ("Bearer realm=%q"):format(module.host .. "/" .. module.name)

-- ---------------------------------------------------------------------------
-- Auth helpers (mirrors mod_http_admin_api/check_auth so callers see one
-- consistent surface).
-- ---------------------------------------------------------------------------

local function check_credentials(request)
    local auth_type, auth_data = string.match(request.headers.authorization or "", "^(%S+)%s(.+)$")
    if not (auth_type and auth_data) then return false end
    if auth_type == "Bearer" then
        return tokens.get_token_session(auth_data)
    end
    return nil
end

local function require_auth(handler)
    return function(event, ...)
        local session = check_credentials(event.request)
        if not session then
            event.response.headers.authorization = www_authenticate_header
            return 401
        end
        session.type = "c2s"
        session.full_jid = jid_join(session.username, session.host, session.resource)
        event.session = session
        if not module:may(":access-admin-api", event) then
            return 403
        end
        return handler(event, ...)
    end
end

-- ---------------------------------------------------------------------------
-- connected_at tracking. Prosody doesn't record session start time anywhere
-- accessible, so we stamp it on resource-bind. Sessions created before this
-- module loads will have a missing timestamp until they reconnect.
-- ---------------------------------------------------------------------------

module:hook("resource-bind", function(event)
    local session = event.session
    if session and not session.connected_at then
        session.connected_at = os.time()
    end
end, 1)

-- ---------------------------------------------------------------------------
-- Serializers
-- ---------------------------------------------------------------------------

local function presence_show(session)
    -- Prosody's presence stanza wraps <show>online/away/dnd/...</show>.
    -- Bare <presence/> with no <show> means "online".
    if not session.presence then return "online" end
    local s = session.presence:get_child_text("show")
    return s or "online"
end

local function session_to_json(session)
    local connected_at = session.connected_at
    local connected_at_iso
    if connected_at then
        connected_at_iso = os.date("!%Y-%m-%dT%H:%M:%SZ", connected_at)
    end
    return {
        jid          = session.full_jid,
        bare_jid     = jid_bare(session.full_jid),
        username     = session.username,
        host         = session.host,
        resource     = session.resource,
        ip_address   = session.ip,
        secure       = session.secure or false,
        priority     = session.priority or 0,
        status       = presence_show(session),
        connected_at = connected_at_iso,  -- nil → omitted by JSON encoder
    }
end

-- ---------------------------------------------------------------------------
-- Endpoint implementations
-- ---------------------------------------------------------------------------

local function list_sessions(event)
    local response = event.response
    local out = {}
    for _, session in pairs(prosody.full_sessions) do
        if session.host == module.host then
            out[#out + 1] = session_to_json(session)
        end
    end
    response.headers.content_type = "application/json"
    return json.encode(out)
end

local function close_one_session(event, jid_path)
    -- The {full_jid} path may be percent-encoded; util.http handles unescape
    -- before passing to handlers. If your Prosody version doesn't, decode here.
    local full_jid = jid_path
    local target = prosody.full_sessions[full_jid]
    if not target or target.host ~= module.host then
        return 404
    end
    target:close({
        condition = "policy-violation";
        text = "Disconnected by administrator";
    })
    return 204
end

local function disconnect_user(event, username)
    if not username or username == "" then return 400 end
    local bare = jid_join(username, module.host)
    local user_sessions = prosody.bare_sessions[bare]
    if not user_sessions then
        return 404
    end
    -- Snapshot first; close() mutates the table.
    local targets = {}
    for _, s in pairs(user_sessions.sessions) do
        targets[#targets + 1] = s
    end
    local closed = 0
    for _, s in ipairs(targets) do
        s:close({
            condition = "policy-violation";
            text = "Disconnected by administrator";
        })
        closed = closed + 1
    end
    event.response.headers.content_type = "application/json"
    return json.encode({ closed = closed })
end

-- ---------------------------------------------------------------------------
-- Route table
-- ---------------------------------------------------------------------------

module:provides("http", {
    default_path = "/admin_sessions";
    cors = {
        enabled = true;
        credentials = true;
    };
    route = {
        ["GET /"]                = require_auth(list_sessions);
        ["DELETE /*"]            = require_auth(close_one_session);
        ["POST /disconnect/*"]   = require_auth(disconnect_user);
    };
})
