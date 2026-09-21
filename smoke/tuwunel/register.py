"""Registers the smoke admin through Tuwunel's shared-secret endpoint (an
existing admin is fine) and prints a fresh access token for it."""
import hashlib
import hmac
import json
import sys
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:8008"
SECRET = b"smoke-registration-secret"
USER, PASSWORD = "admin", "admin-password-smoke"


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req) as resp:
            return resp.status, json.load(resp)
    except urllib.error.HTTPError as err:
        return err.code, json.load(err)


_, nonce = call("GET", "/_synapse/admin/v1/register")
mac = hmac.new(SECRET, digestmod=hashlib.sha1)
mac.update(b"\0".join([nonce["nonce"].encode(), USER.encode(), PASSWORD.encode(), b"admin"]))
status, body = call("POST", "/_synapse/admin/v1/register", {
    "nonce": nonce["nonce"], "username": USER, "password": PASSWORD, "admin": True,
    "mac": mac.hexdigest(), "inhibit_login": True,
})
if status != 200 and body.get("errcode") != "M_USER_IN_USE":
    sys.exit(f"register: {status} {body}")
status, body = call("POST", "/_matrix/client/v3/login", {
    "type": "m.login.password", "identifier": {"type": "m.id.user", "user": USER},
    "password": PASSWORD, "initial_device_display_name": "smoke",
})
if status != 200:
    sys.exit(f"login: {status} {body}")
print(body["access_token"])
