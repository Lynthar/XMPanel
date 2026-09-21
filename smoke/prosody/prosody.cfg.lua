-- Smoke configuration: plaintext c2s on loopback, internal storage, one
-- VirtualHost with the admin API, the panel module and the token helper.
admins = { "admin@example.com" }
plugin_paths = { "/opt/prosody-modules", "/etc/prosody/modules" }
http_default_host = "example.com"

modules_enabled = {
    "roster"; "saslauth"; "tls"; "disco"; "carbons"; "pep"; "private";
    "blocklist"; "vcard4"; "version"; "uptime"; "time"; "ping"; "register";
    "http"; "http_admin_api"; "tokenauth"; "admin_shell";
}
modules_disabled = { "s2s" }

authentication = "internal_hashed"
allow_registration = false
c2s_require_encryption = false
allow_unencrypted_plain_auth = true

http_ports = { 5280 }
http_interfaces = { "*" }
https_ports = {}
c2s_ports = { 5222 }
c2s_interfaces = { "*" }

pidfile = "/var/run/prosody/prosody.pid"
log = { { levels = { min = "info" }, to = "console" } }

VirtualHost "example.com"
    modules_enabled = { "http_admin_api"; "admin_panel"; "admin_token_helper" }
