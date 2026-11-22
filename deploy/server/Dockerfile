# syntax=docker/dockerfile:1
# Build context: repository root (see docker-compose.yml).

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
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/ ./cmd/xs-server ./cmd/xs-ctl ./cmd/xs-hubctl

# Test hub + origin for the smoke test (deploy/server/smoke); not shipped.
FROM alpine:3.22 AS smoke-hub
RUN apk add --no-cache iputils curl busybox-extras
COPY --from=build /out/xs-hubctl /usr/local/bin/

# The server image: xs-server and xs-ctl, nft for the masquerade rule;
# xs-hubctl for the deploy health check (xs-hubctl probe, remote-deploy.sh).
FROM alpine:3.22 AS server
RUN apk add --no-cache nftables
COPY --from=build /out/xs-server /out/xs-ctl /out/xs-hubctl /usr/local/bin/
VOLUME /data
EXPOSE 51900/udp 443/udp 8443/tcp
ENTRYPOINT ["xs-server"]
CMD ["-config", "/data/server.yaml"]
