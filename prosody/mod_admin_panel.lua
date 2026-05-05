-- mod_admin_panel: REST endpoints for XMPP user + session management.
--
-- Prosody 13's mod_http_admin_api covers /server/info, /users (list-only —
-- its PUT does NOT actually create users in 13.0.5), and a few invite/group
-- routes. It does NOT cover live session control or actual account CRUD.
-- This module fills both gaps.
--
-- Endpoints (all under /admin_panel/, host-scoped):
--
--   USER MANAGEMENT
--     GET    /admin_panel/users                    list registered users
--     PUT    /admin_panel/users/{username}         create user (body: {password})
--     DELETE /admin_panel/users/{username}         delete user + purge data
--     PATCH  /admin_panel/users/{username}         change password (body: {password})
--
--   SESSION MANAGEMENT
--     GET    /admin_panel/sessions                 list active c2s
--     DELETE /admin_panel/sessions/{full_jid}      disconnect one session
--     POST   /admin_panel/sessions/disconnect/{username}
--                                                  disconnect all of user
--
-- Auth: same Bearer-token pattern as mod_http_admin_api (mod_tokenauth +
-- prosody:admin permission gate).
--
-- Install:
--   1. Place this file at /etc/prosody/modules/mod_admin_panel.lua
--   2. Add "admin_panel" to the VirtualHost's modules_enabled.
--   3. systemctl restart prosody.

local jid_lib = require "util.jid"
local jid_join = jid_lib.join
local jid_bare = jid_lib.bare
local json = require "util.json"
local usermanager = require "core.usermanager"

local tokens = module:depends("tokenauth")
module:depends("http")

module:default_permission("prosody:admin", ":access-admin-api")

local www_authenticate_header = ("Bearer realm=%q"):format(module.host .. "/" .. module.name)

-- ---------------------------------------------------------------------------
-- Auth (same flow as mod_http_admin_api)
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
-- connected_at tracking. Stamp a timestamp on resource-bind so the panel
-- can show "since" times. Sessions established before this module loads
-- have no timestamp until they reconnect.
-- ---------------------------------------------------------------------------

module:hook("resource-bind", function(event)
    local s = event.session
    if s and not s.connected_at then s.connected_at = os.time() end
end, 1)

-- ---------------------------------------------------------------------------
-- Helpers
-- ---------------------------------------------------------------------------

local function read_json_body(request)
    if not request.body or request.body == "" then return nil end
    local ok, parsed = pcall(json.decode, request.body)
    if not ok then return nil end
    return parsed
end

local function presence_show(session)
    if not session.presence then return "online" end
    return session.presence:get_child_text("show") or "online"
end

local function session_to_json(session)
    local connected_at_iso
    if session.connected_at then
        connected_at_iso = os.date("!%Y-%m-%dT%H:%M:%SZ", session.connected_at)
    end
    return {
        jid          = session.full_jid;
        bare_jid     = jid_bare(session.full_jid);
        username     = session.username;
        host         = session.host;
        resource     = session.resource;
        ip_address   = session.ip;
        secure       = session.secure or false;
        priority     = session.priority or 0;
        status       = presence_show(session);
        connected_at = connected_at_iso;
    }
end

-- ---------------------------------------------------------------------------
-- USER endpoints
-- ---------------------------------------------------------------------------

local function list_users(event)
    local response = event.response
    response.headers.content_type = "application/json"
    local out = {}
    for username in usermanager.users(module.host) do
        out[#out + 1] = {
            username = username;
            jid      = jid_join(username, module.host);
        }
    end
    return json.encode(out)
end

local function create_user(event, username)
    if not username or username == "" then return 400 end
    local body = read_json_body(event.request)
    if not body or type(body.password) ~= "string" or body.password == "" then
        return 400
    end
    if usermanager.user_exists(username, module.host) then
        return 409
    end
    local ok, err = usermanager.create_user(username, body.password, module.host)
    if not ok then
        module:log("warn", "create_user failed for %s@%s: %s", username, module.host, tostring(err))
        return 500
    end
    event.response.status_code = 201
    event.response.headers.content_type = "application/json"
    return json.encode({
        username = username;
        jid      = jid_join(username, module.host);
    })
end

local function delete_user(event, username)
    if not username or username == "" then return 400 end
    if not usermanager.user_exists(username, module.host) then
        return 404
    end
    local ok, err = usermanager.delete_user(username, module.host)
    if not ok then
        module:log("warn", "delete_user failed for %s@%s: %s", username, module.host, tostring(err))
        return 500
    end
    return 204
end

local function set_password(event, username)
    if not username or username == "" then return 400 end
    if not usermanager.user_exists(username, module.host) then
        return 404
    end
    local body = read_json_body(event.request)
    if not body or type(body.password) ~= "string" or body.password == "" then
        return 400
    end
    local ok, err = usermanager.set_password(username, body.password, module.host)
    if not ok then
        module:log("warn", "set_password failed for %s@%s: %s", username, module.host, tostring(err))
        return 500
    end
    return 204
end

-- ---------------------------------------------------------------------------
-- SESSION endpoints
-- ---------------------------------------------------------------------------

local function list_sessions(event)
    local response = event.response
    response.headers.content_type = "application/json"
    local out = {}
    for _, session in pairs(prosody.full_sessions) do
        if session.host == module.host then
            out[#out + 1] = session_to_json(session)
        end
    end
    return json.encode(out)
end

local function close_session(event, full_jid)
    local target = prosody.full_sessions[full_jid]
    if not target or target.host ~= module.host then return 404 end
    target:close({ condition = "policy-violation"; text = "Disconnected by administrator" })
    return 204
end

local function disconnect_user(event, username)
    if not username or username == "" then return 400 end
    local bare = jid_join(username, module.host)
    local user_sessions = prosody.bare_sessions[bare]
    if not user_sessions then return 404 end
    local targets = {}
    for _, s in pairs(user_sessions.sessions) do targets[#targets + 1] = s end
    local closed = 0
    for _, s in ipairs(targets) do
        s:close({ condition = "policy-violation"; text = "Disconnected by administrator" })
        closed = closed + 1
    end
    event.response.headers.content_type = "application/json"
    return json.encode({ closed = closed })
end

-- ---------------------------------------------------------------------------
-- Routes
-- ---------------------------------------------------------------------------

module:provides("http", {
    default_path = "/admin_panel";
    cors = { enabled = true; credentials = true; };
    route = {
        ["GET /users"]                    = require_auth(list_users);
        ["PUT /users/*"]                  = require_auth(create_user);
        ["DELETE /users/*"]               = require_auth(delete_user);
        ["PATCH /users/*"]                = require_auth(set_password);

        ["GET /sessions"]                 = require_auth(list_sessions);
        ["DELETE /sessions/*"]            = require_auth(close_session);
        ["POST /sessions/disconnect/*"]   = require_auth(disconnect_user);
    };
})
