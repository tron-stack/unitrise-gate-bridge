VERSION ?= 0.5.0
LDFLAGS := -s -w -X github.com/mytruckyards/unitrise-gate-bridge/internal/api.AgentVersion=$(VERSION)
# The tray is a GUI companion: -H=windowsgui keeps a login-launched tray from
# flashing a console window (the exact papercut it exists to remove).
TRAY_LDFLAGS := $(LDFLAGS) -H=windowsgui

.PHONY: build release sign test clean winres

build:
	go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate ./cmd/unitrise-gate

# Windows version resource: stamps ProductName/CompanyName/FileVersion into
# the exe so Explorer, Task Manager and incident responders can identify it.
# The _windows_amd64 suffix makes Go link it ONLY into windows/amd64 builds.
VMAJOR := $(word 1,$(subst ., ,$(VERSION)))
VMINOR := $(word 2,$(subst ., ,$(VERSION)))
VPATCH := $(word 3,$(subst ., ,$(VERSION)))
winres:
	go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.4.1 \
		-ver-major=$(VMAJOR) -ver-minor=$(VMINOR) -ver-patch=$(VPATCH) \
		-product-ver-major=$(VMAJOR) -product-ver-minor=$(VMINOR) -product-ver-patch=$(VPATCH) \
		-file-version=$(VERSION) -product-version=$(VERSION) -64 \
		-o cmd/unitrise-gate/resource_windows_amd64.syso versioninfo.json
	go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.4.1 \
		-ver-major=$(VMAJOR) -ver-minor=$(VMINOR) -ver-patch=$(VPATCH) \
		-product-ver-major=$(VMAJOR) -product-ver-minor=$(VMINOR) -product-ver-patch=$(VPATCH) \
		-file-version=$(VERSION) -product-version=$(VERSION) -64 \
		-o cmd/unitrise-gate-tray/resource_windows_amd64.syso versioninfo.json

# Release checklist (the normal path is CI - .github/workflows/release.yml):
#   1. git tag v<x.y.z> && git push origin v<x.y.z>
#      → CI runs make test + make release and publishes the GitHub Release
#        with dist/* attached (asset names match the backend's platform map).
#   2. On the backend (Render env):
#        GATE_BRIDGE_DOWNLOAD_BASE=https://github.com/tron-stack/unitrise-gate-bridge/releases/latest/download
#        GATE_AGENT_LATEST_VERSION=x.y.z     (no leading v)
#      The base never changes between releases; only the version env moves.
#      update-check, the console's update chip, the download links, and
#      `unitrise-gate update` all key off those two env vars.
#   3. Signing (optional, local): make sign, then re-upload the signed
#      artifacts to the release (signing changes the bytes AND the sums).
# NOTE: GitHub release assets are only anonymously downloadable when the repo
# is PUBLIC. Private repo = host dist/* on a public bucket instead and point
# GATE_BRIDGE_DOWNLOAD_BASE there.
# Manual fallback: make release VERSION=x.y.z && upload dist/* yourself.
release: clean winres
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-windows-amd64.exe ./cmd/unitrise-gate
	GOOS=windows GOARCH=amd64 go build -ldflags "$(TRAY_LDFLAGS)" -o dist/unitrise-gate-tray-windows-amd64.exe ./cmd/unitrise-gate-tray
	cp scripts/install.ps1 scripts/uninstall.ps1 dist/
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-darwin-arm64 ./cmd/unitrise-gate
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-darwin-amd64 ./cmd/unitrise-gate
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-linux-amd64 ./cmd/unitrise-gate
	cd dist && shasum -a 256 unitrise-gate-* *.ps1 > SHA256SUMS
	@ls -lh dist/
# NOTE: the darwin TRAY builds are absent above on purpose - the macOS menu
# bar needs cgo/Cocoa, which the ubuntu release runner can't cross-compile.
# They ship with the Mac release pass (macos runner + Developer ID signing):
# CGO_ENABLED=1 is explicit: Go silently disables cgo when GOARCH differs
# from the host, and the menu bar is Cocoa - Apple's clang cross-compiles
# both arches fine.
tray-darwin:
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-tray-darwin-arm64 ./cmd/unitrise-gate-tray
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-tray-darwin-amd64 ./cmd/unitrise-gate-tray

# Code signing hooks - both are OPTIONAL and no-op unless configured.
#   Windows Authenticode: set SIGNTOOL (path to signtool.exe or osslsigncode
#     wrapper script) - needs a purchased code-signing cert; unsigned builds
#     trip SmartScreen on first run (site IT clicks through "More info").
#   macOS: set CODESIGN_IDENTITY ("Developer ID Application: ...").
# Re-run `cd dist && shasum -a 256 unitrise-gate-* *.ps1 > SHA256SUMS` after signing
# (signing changes the bytes).
sign:
ifdef SIGNTOOL
	$(SIGNTOOL) dist/unitrise-gate-windows-amd64.exe
	$(SIGNTOOL) dist/unitrise-gate-tray-windows-amd64.exe
endif
ifdef CODESIGN_IDENTITY
	codesign --force --options runtime -s "$(CODESIGN_IDENTITY)" dist/unitrise-gate-darwin-arm64
	codesign --force --options runtime -s "$(CODESIGN_IDENTITY)" dist/unitrise-gate-darwin-amd64
endif
	cd dist && shasum -a 256 unitrise-gate-* *.ps1 > SHA256SUMS

test:
	go vet ./...
	go test ./...

clean:
	rm -rf dist && mkdir -p dist
