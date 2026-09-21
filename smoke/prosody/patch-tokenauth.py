import re
import sys

path = sys.argv[1]
with open(path) as f:
    source = f.read()
pattern = re.compile(r'local function select_role\(username, host, role_name\).*?\nend', re.DOTALL)
replacement = '''local function select_role(username, host, role_name)
    if not role_name then return end
    local role = usermanager.get_role_by_name(role_name, host)
    if not role then return end
    return role
end'''
patched, count = pattern.subn(replacement, source, count=1)
if count != 1:
    sys.exit("select_role not found in " + path)
with open(path, "w") as f:
    f.write(patched)
