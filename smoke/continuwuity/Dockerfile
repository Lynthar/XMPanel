# Continuwuity from the static binary on its Forgejo release page (no Docker
# Hub pull), on the same Ubuntu base as the other images. It stands in for
# every homeserver the panel only knows through the specification; the admin
# is created by a startup command and its token is made on start.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ARG CONTINUWUITY_VERSION=v26.9.0
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl python3 \
 && rm -rf /var/lib/apt/lists/* \
 && case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;; esac \
 && curl -fsSL "https://forgejo.ellis.link/continuwuation/continuwuity/releases/download/${CONTINUWUITY_VERSION}/conduwuit-linux-static-${arch}" -o /usr/local/bin/continuwuity \
 && chmod +x /usr/local/bin/continuwuity \
 && mkdir -p /var/lib/continuwuity /etc/continuwuity

COPY smoke.toml /etc/continuwuity/smoke.toml
COPY entrypoint.sh /smoke-entrypoint.sh
RUN chmod +x /smoke-entrypoint.sh

EXPOSE 8008
ENTRYPOINT ["/smoke-entrypoint.sh"]
