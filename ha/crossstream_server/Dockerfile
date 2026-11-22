# CrossStream server as a Home Assistant app. One Dockerfile for both paths:
# the release pipeline (buildx, linux/amd64 + linux/arm64) and a local build by
# the Supervisor from the bundle (make ha-bundle). The static binaries are
# already in bin/amd64 and bin/aarch64 (CGO_ENABLED=0); nothing is compiled here.
ARG TARGETARCH

FROM alpine:3.22 AS arch-amd64
LABEL io.hass.arch="amd64"

FROM alpine:3.22 AS arch-arm64
LABEL io.hass.arch="aarch64"

FROM arch-${TARGETARCH}
ARG TARGETARCH
ARG BUILD_VERSION=0.0.0-dev
RUN apk add --no-cache nftables jq
COPY bin/ /tmp/bin/
RUN arch="$TARGETARCH"; [ "$arch" = arm64 ] && arch=aarch64; \
    install -m 0755 "/tmp/bin/$arch/xs-server" "/tmp/bin/$arch/xs-ctl" /usr/local/bin/ && rm -rf /tmp/bin
COPY run.sh /run.sh
RUN chmod 0755 /run.sh
LABEL \
    io.hass.type="app" \
    io.hass.version="${BUILD_VERSION}" \
    io.hass.name="CrossStream Server" \
    io.hass.description="Bonding server for CrossStream hubs (multi-phone cellular bonding)" \
    io.hass.url="https://github.com/zyndata/cross-stream" \
    org.opencontainers.image.title="Home Assistant App: CrossStream Server" \
    org.opencontainers.image.source="https://github.com/zyndata/cross-stream"
ENTRYPOINT ["/run.sh"]
