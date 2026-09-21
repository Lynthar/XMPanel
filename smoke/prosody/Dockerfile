# Prosody 13 from the project's own apt repository, plus the community admin
# API module (and mod_groups_internal, which it depends on) and the panel's
# module, set up the way the deployment guide does it: token auth with the
# select_role patch, an admin account, and an admin bearer token written on
# start. Built from Ubuntu rather than the Docker Hub image so the only
# registry pull is the base.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl gnupg python3 \
 && curl -fsSL https://prosody.im/files/prosody-debian-packages.key | gpg --dearmor -o /etc/apt/trusted.gpg.d/prosody.gpg \
 && echo "deb https://packages.prosody.im/debian noble main" > /etc/apt/sources.list.d/prosody.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends prosody lua5.4 \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /opt/prosody-modules \
 && for m in mod_http_admin_api mod_groups_internal; do \
      curl -fsSL "https://hg.prosody.im/prosody-modules/archive/tip.tar.gz/$m" \
        | tar -xz --strip-components=1 -C /opt/prosody-modules; \
    done \
 && ls /opt/prosody-modules/mod_http_admin_api/mod_http_admin_api.lua /opt/prosody-modules/mod_groups_internal/mod_groups_internal.lua

# mod_tokenauth's select_role rejects valid admin tokens on 13.0.x; the
# deployment guide applies the same five-line replacement.
COPY patch-tokenauth.py /tmp/patch-tokenauth.py
RUN python3 /tmp/patch-tokenauth.py /usr/lib/prosody/modules/mod_tokenauth.lua \
 && (luac5.4 -p /usr/lib/prosody/modules/mod_tokenauth.lua || lua5.4 -e "loadfile('/usr/lib/prosody/modules/mod_tokenauth.lua')")

COPY prosody.cfg.lua /etc/prosody/prosody.cfg.lua
COPY mod_admin_token_helper.lua /etc/prosody/modules/mod_admin_token_helper.lua
COPY entrypoint.sh /smoke-entrypoint.sh
RUN chmod +x /smoke-entrypoint.sh && chown -R prosody:prosody /etc/prosody /var/lib/prosody

EXPOSE 5222 5280
ENTRYPOINT ["/smoke-entrypoint.sh"]
