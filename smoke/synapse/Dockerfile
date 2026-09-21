# Synapse from the matrix.org apt repository (what its docs recommend on
# Debian and Ubuntu), on the same Ubuntu base as the other two images so the
# only registry pull is the base; the admin and its token are made on start.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl gnupg python3 \
 && curl -fsSL https://packages.matrix.org/debian/matrix-org-archive-keyring.gpg -o /usr/share/keyrings/matrix-org-archive-keyring.gpg \
 && echo "deb [signed-by=/usr/share/keyrings/matrix-org-archive-keyring.gpg] https://packages.matrix.org/debian/ noble main" > /etc/apt/sources.list.d/matrix-org.list \
 && echo "matrix-synapse-py3 matrix-synapse/server-name string example.com" | debconf-set-selections \
 && echo "matrix-synapse-py3 matrix-synapse/report-stats boolean false" | debconf-set-selections \
 && apt-get update \
 && apt-get install -y --no-install-recommends matrix-synapse-py3 \
 && rm -rf /var/lib/apt/lists/*

COPY smoke.yaml /etc/matrix-synapse/conf.d/smoke.yaml
COPY log.yaml /etc/matrix-synapse/log-smoke.yaml
COPY entrypoint.sh /smoke-entrypoint.sh
RUN chmod +x /smoke-entrypoint.sh

EXPOSE 8008
ENTRYPOINT ["/smoke-entrypoint.sh"]
