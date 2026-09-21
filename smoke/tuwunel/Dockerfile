# Tuwunel from its GitHub release package (no Docker Hub pull), on the same
# Ubuntu base as the other images. The package is unpacked, not installed,
# so its postinst (service user, systemd) does not run; the admin and its
# token are made on start.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ARG TUWUNEL_VERSION=v1.9.2
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl python3 \
 && rm -rf /var/lib/apt/lists/* \
 && case "$(uname -m)" in x86_64) arch=x86_64-v1 ;; aarch64) arch=aarch64-v8 ;; *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;; esac \
 && curl -fsSL "https://github.com/matrix-construct/tuwunel/releases/download/${TUWUNEL_VERSION}/${TUWUNEL_VERSION}-release-all-${arch}-linux-gnu-tuwunel.deb" -o /tmp/tuwunel.deb \
 && dpkg-deb -x /tmp/tuwunel.deb / \
 && rm /tmp/tuwunel.deb \
 && mkdir -p /var/lib/tuwunel

COPY smoke.toml /etc/tuwunel/smoke.toml
COPY register.py /smoke-register.py
COPY entrypoint.sh /smoke-entrypoint.sh
RUN chmod +x /smoke-entrypoint.sh

EXPOSE 8008
ENTRYPOINT ["/smoke-entrypoint.sh"]
