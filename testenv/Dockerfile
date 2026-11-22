# syntax=docker/dockerfile:1
# E2E test image (docs/testing.md): every CrossStream binary plus the network
# tools the scenarios need. One image for server, hub, relays, carriers and
# client. Build context: repository root.

# Web panel (phase 15): Svelte build, checked against the panel's CSP
# (scripts/csp-check.mjs fails the build on any violation).
FROM node:24-alpine AS webui
WORKDIR /webui
COPY webui/package.json webui/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY webui/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY webui/assets.go ./webui/
COPY --from=webui /webui/dist ./webui/dist
ARG VERSION=dev
# RACE=1 (run.sh): binaries with the race detector (cgo, musl).
ARG RACE=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    if [ "$RACE" = 1 ]; then \
        apk add --no-cache gcc musl-dev >/dev/null && \
        CGO_ENABLED=1 go build -race -trimpath -ldflags "-X main.version=${VERSION}" -o /out/ ./cmd/...; \
    else \
        CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ ./cmd/...; \
    fi

FROM alpine:3.22 AS node
RUN apk add --no-cache iproute2 iproute2-tc nftables curl iputils iperf3 jq procps dnsmasq busybox-extras
# Scenario "update" runs the real deploy/hub/install.sh: GNU install, groupadd, getent.
RUN apk add --no-cache coreutils shadow musl-utils
COPY --from=build /out/ /usr/local/bin/
# Race reports (RACE=1 builds only) go to files that run.sh collects.
ENV GORACE="log_path=/tmp/race"
COPY --chmod=0755 testenv/bin/ /usr/local/bin/
