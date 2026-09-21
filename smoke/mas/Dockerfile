# Matrix Authentication Service from its release tarball (no Docker Hub
# pull), on the same Ubuntu base as the other images. The config is written
# in full (MAS appends list items across config files rather than replacing
# them), with the secrets generated at build time into a second file.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ARG MAS_VERSION=v1.24.0
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl openssl \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /opt/mas /var/lib/mas \
 && curl -fsSL "https://github.com/element-hq/matrix-authentication-service/releases/download/${MAS_VERSION}/mas-cli-$(uname -m)-linux.tar.gz" \
    | tar -xz -C /opt/mas \
 && openssl genrsa -out /opt/mas/rsa.key 2048 \
 && printf 'secrets:\n  encryption: %s\n  keys:\n    - kid: smoke\n      key_file: /opt/mas/rsa.key\n' "$(openssl rand -hex 32)" > /opt/mas/secrets.yaml

COPY smoke.yaml /opt/mas/smoke.yaml
COPY entrypoint.sh /smoke-entrypoint.sh
RUN chmod +x /smoke-entrypoint.sh

WORKDIR /opt/mas
EXPOSE 8080
ENTRYPOINT ["/smoke-entrypoint.sh"]
