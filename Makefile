# CrossStream build entry points. Recipes need a POSIX sh (Git Bash on Windows).
# Every target prints only failures and one final OK line.

SHELL   := sh
GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test test-tpm fuzz lint changelog aar apk gui hub-windows gui-test webui e2e docker-server server-image server-smoke public-check public-export ha-app ha-bundle ha-upload ha-image ha-smoke clean

build:
	@mkdir -p bin
	@$(GO) build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...
	@echo "build OK ($(VERSION))"

test:
	@out=$$($(GO) test -count=1 ./... 2>&1) || { \
		echo "$$out" | grep -v -e '^ok ' -e 'no test files'; exit 1; }
	@echo "test OK"

# TPM sealing against the go-tpm-tools simulator: needs cgo (gcc) and OpenSSL headers (libssl-dev).
test-tpm:
	@out=$$(CGO_ENABLED=1 $(GO) test -count=1 -tags tpmsim -run 'Simulator|TPM' ./internal/hub/tpmkey/ ./internal/hub/ 2>&1) || { \
		echo "$$out" | grep -v -e '^ok '; exit 1; }
	@echo "test-tpm OK"

# Runs every Fuzz* target in FUZZPKGS for FUZZTIME each (go test -fuzz takes one target per run).
FUZZTIME ?= 30s
FUZZPKGS ?= ./internal/wire ./internal/dns ./internal/control
fuzz:
	@for pkg in $(FUZZPKGS); do \
		for f in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			out=$$($(GO) test -run='^$$' -fuzz="^$$f\$$" -fuzztime=$(FUZZTIME) $$pkg 2>&1) || { \
				echo "$$out" | tail -n 20; exit 1; }; \
			echo "fuzz $$f OK ($(FUZZTIME))"; \
		done; \
	done

lint:
	@out=$$($(GO) vet ./... 2>&1) || { echo "$$out"; exit 1; }
	@if command -v golangci-lint >/dev/null 2>&1; then \
		out=$$(golangci-lint run ./... 2>&1) || { echo "$$out"; exit 1; }; \
	else echo "golangci-lint not installed: ran go vet only"; fi
	@echo "lint OK"

changelog:
	@if ! command -v git-cliff >/dev/null 2>&1; then \
		echo "git-cliff not installed: see docs/DEVELOPMENT.md"; exit 1; fi
	@out=$$(git-cliff -o CHANGELOG.md 2>&1) || { echo "$$out"; exit 1; }
	@echo "changelog OK"

# Relay core for the Android app (gomobile, pinned in go.mod as a tool).
# Needs ANDROID_HOME (SDK with a platform) and ANDROID_NDK_HOME (NDK r26+); see docs/DEVELOPMENT.md.
AAR_TARGETS ?= android/arm64,android/arm,android/amd64
aar:
	@mkdir -p bin android/app/libs
	@GOBIN="$(CURDIR)/bin" $(GO) install golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind
	@out=$$(PATH="$$PWD/bin:$$PATH" gomobile bind -target=$(AAR_TARGETS) -androidapi 29 \
		-javapkg com.zyndata.crossstream.core -trimpath -ldflags "-s -w -extldflags=-Wl,-z,max-page-size=16384" \
		-o android/app/libs/xscore.aar ./mobile 2>&1) || { echo "$$out" | tail -n 30; exit 1; }
	@echo "aar OK (android/app/libs/xscore.aar)"

# Signed release APK for sideloading: needs the release.* keys in android/local.properties
# (docs/DEVELOPMENT.md); writes bin/crossstream-relay-<versionName>.apk.
apk: aar
	@grep -qs '^release.storeFile=' android/local.properties || \
		{ echo "no release.* keys in android/local.properties (docs/DEVELOPMENT.md)"; exit 1; }
	@ver=$$(sed -n 's/.*versionName = "\(.*\)".*/\1/p' android/app/build.gradle.kts); \
	out=$$(cd android && ./gradlew --console=plain assembleRelease 2>&1) || { echo "$$out" | tail -n 30; exit 1; }; \
	apk=android/app/build/outputs/apk/release/app-release.apk; \
	cp "$$apk" "bin/crossstream-relay-$$ver.apk" && echo "apk OK (bin/crossstream-relay-$$ver.apk)"

# Hub desktop app (Wails). gui/ is its own module; the Wails CLI is a go tool there.
# Linux needs libgtk-3-dev, libwebkit2gtk-4.1-dev and Node.js (docs/DEVELOPMENT.md).
gui:
	@tags=; [ "$$(uname -s)" = Linux ] && tags="-tags webkit2_41"; \
	out=$$(cd gui && $(GO) tool wails build -clean -trimpath $$tags -ldflags "-X main.version=$(VERSION)" 2>&1) || \
		{ echo "$$out" | tail -n 40; exit 1; }; \
	mkdir -p bin && cp gui/build/bin/xs-hub-gui* bin/ && echo "gui OK (bin/xs-hub-gui, $(VERSION))"

# Windows hub binaries (cross-compiled from any OS) + the signed Wintun driver DLL,
# checked against its published SHA-256: bin/windows/ is what install.ps1 copies.
WINTUN_VER    := 0.14.1
WINTUN_SHA256 := 07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51
hub-windows:
	@mkdir -p bin/windows
	@out=$$(GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/windows/ ./cmd/xs-hubd ./cmd/xs-hubctl 2>&1) || \
		{ echo "$$out"; exit 1; }
	@[ -f bin/windows/wintun.dll ] || { \
		curl -sSfL -o bin/windows/wintun.zip https://www.wintun.net/builds/wintun-$(WINTUN_VER).zip && \
		echo "$(WINTUN_SHA256)  bin/windows/wintun.zip" | sha256sum -c --quiet - && \
		unzip -q -j -o bin/windows/wintun.zip wintun/bin/amd64/wintun.dll -d bin/windows; } || \
		{ echo "wintun $(WINTUN_VER) download or checksum failed"; exit 1; }
	@rm -f bin/windows/wintun.zip
	@cp deploy/hub-windows/install.ps1 bin/windows/
	@echo "hub-windows OK (bin/windows: xs-hubd.exe, xs-hubctl.exe, wintun.dll $(WINTUN_VER), install.ps1)"

gui-test:
	@out=$$(cd gui && $(GO) vet . 2>&1 && $(GO) test -count=1 . 2>&1) || { echo "$$out" | tail -n 40; exit 1; }
	@echo "gui-test OK"

# Server web panel (webui/, Svelte + Vite): build into webui/dist, embedded by
# the next go build of xs-server; fails on any CSP violation (scripts/csp-check.mjs).
webui:
	@out=$$(cd webui && npm ci --no-audit --no-fund 2>&1 && npm run build 2>&1) || { echo "$$out" | tail -n 40; exit 1; }
	@echo "webui OK ($$(cd webui && node scripts/csp-check.mjs))"

# E2E scenarios in Docker (testenv/, docs/testing.md). NETEM=0 if the kernel lacks sch_netem.
e2e:
	@sh testenv/run.sh all

# Server image (xs-server + xs-ctl); see deploy/server/README.md.
docker-server:
	@out=$$(docker build -q -f deploy/server/Dockerfile --target server \
		--build-arg VERSION=$(VERSION) -t crossstream/xs-server:$(VERSION) . 2>&1) || { echo "$$out"; exit 1; }
	@echo "docker-server OK (crossstream/xs-server:$(VERSION))"

# The server image as a tarball for deploy/server/remote-deploy.sh (docker load on
# the server, no registry): bin/xs-server-image-<VERSION>-amd64.tar.gz.
server-image: docker-server
	@mkdir -p bin
	@docker save -o bin/xs-server-image-$(VERSION)-amd64.tar crossstream/xs-server:$(VERSION)
	@gzip -nf bin/xs-server-image-$(VERSION)-amd64.tar
	@echo "server-image OK (bin/xs-server-image-$(VERSION)-amd64.tar.gz)"

# Phase 02 acceptance in Docker: hub -> server -> origin containers.
server-smoke:
	@sh deploy/server/smoke/run.sh

# Home Assistant app (ha/crossstream_server, phase 17b): the app folder in bin/ha with
# static xs-server + xs-ctl (panel embedded) in bin/amd64 and bin/aarch64.
HA_VERSION ?= $(patsubst v%,%,$(VERSION))
ha-app: webui
	@rm -rf bin/ha
	@mkdir -p bin/ha && cp -R ha/crossstream_server bin/ha/
	@for a in amd64:amd64 arm64:aarch64; do \
		out=$$(GOOS=linux GOARCH=$${a%%:*} CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
			-o bin/ha/crossstream_server/bin/$${a##*:}/ ./cmd/xs-server ./cmd/xs-ctl 2>&1) || { echo "$$out"; exit 1; }; \
	done
	@sed -i.bak 's/^version: .*/version: "$(HA_VERSION)"/' bin/ha/crossstream_server/config.yaml
	@rm -f bin/ha/crossstream_server/config.yaml.bak
	@echo "ha-app OK (bin/ha/crossstream_server, version $(HA_VERSION))"

# Local bundle: unpack into /addons/ on the HA box, install from "Local apps"
# (no image: line, so the Supervisor builds it there from the shipped binaries).
ha-bundle: ha-app
	@sed -i.bak '/^image:/d' bin/ha/crossstream_server/config.yaml
	@rm -f bin/ha/crossstream_server/config.yaml.bak
	@tar -czf bin/crossstream-ha-app-$(VERSION).tar.gz -C bin/ha crossstream_server
	@list=$$(tar -tzf bin/crossstream-ha-app-$(VERSION).tar.gz); for f in config.yaml Dockerfile run.sh \
		bin/amd64/xs-server bin/amd64/xs-ctl bin/aarch64/xs-server bin/aarch64/xs-ctl; do \
		echo "$$list" | grep -qx "crossstream_server/$$f" || { echo "ha-bundle: $$f missing"; exit 1; }; done
	@! grep -q '^image:' bin/ha/crossstream_server/config.yaml || { echo "ha-bundle: image: line left"; exit 1; }
	@echo "ha-bundle OK (bin/crossstream-ha-app-$(VERSION).tar.gz)"

# The bundle unpacked into /addons on a HA box through its SSH app (no scp/sftp needed),
# e.g. make ha-upload HA_SSH="ssh -p 22 user@homeassistant.local"; then install or rebuild
# the app under "Local apps".
HA_SSH ?=
ha-upload: ha-bundle
	@[ -n "$(HA_SSH)" ] || { echo "ha-upload: set HA_SSH (ssh command for the HA box)"; exit 1; }
	@$(HA_SSH) 'if [ "$$(id -u)" = 0 ]; then tar -xzf - -C /addons --no-same-owner; \
		else sudo -n tar -xzf - -C /addons --no-same-owner; fi && ls /addons/crossstream_server/config.yaml >/dev/null' \
		<bin/crossstream-ha-app-$(VERSION).tar.gz || { echo "ha-upload failed"; exit 1; }
	@echo "ha-upload OK (/addons/crossstream_server, version $(HA_VERSION))"

# The app image (ha-smoke), this machine's architecture unless HA_PLATFORM=linux/arm64
# (needs QEMU/binfmt); releases build both platforms with buildx (deploy.yml).
HA_PLATFORM ?=
ha-image: ha-app
	@out=$$(docker buildx build -q --load $(if $(HA_PLATFORM),--platform $(HA_PLATFORM)) \
		--build-arg BUILD_VERSION=$(HA_VERSION) -t crossstream/ha-app:smoke bin/ha/crossstream_server 2>&1) || \
		{ echo "$$out"; exit 1; }
	@echo "ha-image OK (crossstream/ha-app:smoke $$(docker image inspect crossstream/ha-app:smoke \
		--format '{{.Architecture}} io.hass.arch={{index .Config.Labels "io.hass.arch"}} type={{index .Config.Labels "io.hass.type"}} version={{index .Config.Labels "io.hass.version"}}'))"

# The app run the way the Supervisor runs it, without Home Assistant (ha/smoke/run.sh).
ha-smoke: ha-image
	@sh ha/smoke/run.sh

# Public export (docs/decisions.md, phase 17b): only allowlisted files, no forbidden content.
public-check:
	@sh deploy/public/export.sh check

public-export:
	@rm -rf bin/public
	@sh deploy/public/export.sh export bin/public $(VERSION)

clean:
	@rm -rf bin
	@echo "clean OK"
