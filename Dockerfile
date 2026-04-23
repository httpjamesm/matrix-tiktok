# matrix-tiktok — Matrix ↔ TikTok DM bridge (mautrix bridgev2)
#
# Build:
#   docker build -t matrix-tiktok .
#
# Run (mount your bbctl-generated config as /data/config.yaml):
#   docker run --rm -v /path/to/config.yaml:/data/config.yaml:ro matrix-tiktok
#
# Port publishing (-p) is only needed when homeserver.websocket is false and the
# bridge listens for appservice HTTP transactions (see appservice.port). Beeper /
# websocket setups usually do not listen on appservice.port at all.

FROM golang:1.25-bookworm AS builder

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        gcc libc6-dev libsqlite3-dev libolm-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=docker
ARG COMMIT=unknown
ARG BUILDTIME=unknown

RUN MAUTRIX_VERSION="$(grep 'maunium.net/go/mautrix ' go.mod | awk '{print $2}' | head -n1)" \
    && CGO_ENABLED=1 go build -trimpath \
        -ldflags="-s -w \
            -X main.Tag=${VERSION} \
            -X main.Commit=${COMMIT} \
            -X main.BuildTime=${BUILDTIME} \
            -X maunium.net/go/mautrix.GoModVersion=${MAUTRIX_VERSION}" \
        -o /out/matrix-tiktok \
        ./cmd/matrix-tiktok

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates libsqlite3-0 libolm3 tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --home-dir /data --shell /usr/sbin/nologin bridge \
    && install -d -o bridge -g bridge /data

COPY --from=builder /out/matrix-tiktok /usr/local/bin/matrix-tiktok

USER bridge
WORKDIR /data

VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/matrix-tiktok"]
CMD ["-c", "/data/config.yaml"]
