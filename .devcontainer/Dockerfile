FROM mcr.microsoft.com/devcontainers/go:1.25

USER root

RUN apt-get update && export DEBIAN_FRONTEND=noninteractive \
    && apt-get install -y --no-install-recommends \
        python3 \
        python3-venv \
        ffmpeg \
        postgresql-client \
        libolm-dev \
        ca-certificates \
        curl \
    && ARCH="$(dpkg --print-architecture)" \
    && case "$ARCH" in \
        amd64) BBCTL_ARCH=amd64 ;; \
        arm64) BBCTL_ARCH=arm64 ;; \
        *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /usr/local/bin/bbctl \
        "https://github.com/beeper/bridge-manager/releases/latest/download/bbctl-linux-${BBCTL_ARCH}" \
    && chmod +x /usr/local/bin/bbctl \
    && rm -rf /var/lib/apt/lists/*

USER vscode
