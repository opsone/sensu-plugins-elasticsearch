FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends curl ca-certificates python3 \
 && rm -rf /var/lib/apt/lists/*
COPY check-elasticsearch-status /usr/local/bin/
CMD ["sleep", "infinity"]
