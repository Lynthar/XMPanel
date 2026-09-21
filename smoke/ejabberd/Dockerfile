# ejabberd from Ubuntu's repository with the smoke configuration; built from
# the same base as the Prosody image so no Docker Hub image is needed.
# erlang-p1-oauth2 is only suggested by the package, but oauth_issue_token
# needs it.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends ejabberd erlang-p1-oauth2 \
 && rm -rf /var/lib/apt/lists/*

COPY ejabberd.yml /etc/ejabberd/ejabberd.yml
COPY entrypoint.sh /smoke-entrypoint.sh
RUN chmod +x /smoke-entrypoint.sh && chown ejabberd:ejabberd /etc/ejabberd/ejabberd.yml

EXPOSE 5222 5280
ENTRYPOINT ["/smoke-entrypoint.sh"]
