VERSION ?= 0.6.0
LDFLAGS := -s -w -X github.com/mytruckyards/unitrise-gate-bridge/internal/api.AgentVersion=$(VERSION)

# The release PUBLIC key (hex Ed25519, committed as release-signing.pub) is
# compiled into every build: the agent installs a self-update only when its
# SHA256SUMS carries a signature from the matching private key
# (internal/update). With no release-signing.pub the build carries no key and
# refuses every self-update - `unitrise-gate version` says which.
SIGNING_PUB ?= $(strip $(shell cat release-signing.pub 2>/dev/null))
ifneq ($(SIGNING_PUB),)
LDFLAGS += -X github.com/mytruckyards/unitrise-gate-bridge/internal/update.releasePublicKey=$(SIGNING_PUB)
endif
# ONE Windows exe = installer + agent + tray + dashboard. GUI subsystem
# (-H=windowsgui) so double-click and the login tray never flash a console;
# CLI commands re-attach to the parent terminal (console_windows.go).
WIN_LDFLAGS := $(LDFLAGS) -H=windowsgui

.PHONY: build release sign sign-sums test clean winres

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
		-icon=internal/trayicon/assets/app.ico \
		-manifest=build/app.manifest \
		-o cmd/unitrise-gate/resource_windows_amd64.syso versioninfo.json

# Release checklist (the normal path is CI - .github/workflows/release.yml):
#   1. git tag v<x.y.z> && git push origin v<x.y.z>
#      → CI runs make test + make release and publishes the GitHub Release
#        with dist/* attached (asset names match the backend's platform map).
#   2. On the backend (Render env):
#        GATE_BRIDGE_DOWNLOAD_BASE=https://github.com/tron-stack/unitrise-gate-bridge/releases/latest/download
#        GATE_AGENT_LATEST_VERSION=x.y.z     (no leading v)
#      The installer download links use the "latest" base. A SELF-UPDATE must
#      come from the versioned release folder
#        https://github.com/tron-stack/unitrise-gate-bridge/releases/download/v<x.y.z>/
#      (the agent's compiled-in internal/update ReleaseBase), so the backend's
#      update-check builds its downloadUrl and sha256Url from that folder.
#   3. Release signature (REQUIRED for self-update): CI writes the
#      RELEASE_SIGNING_KEY secret to a file and passes its path as
#      RELEASE_SIGNING_KEY=<path>, so `make release` signs SHA256SUMS into
#      SHA256SUMS.sig. Locally: make release VERSION=x.y.z RELEASE_SIGNING_KEY=~/keys/release-signing.key
#      Agents refuse an update whose SHA256SUMS.sig is missing or doesn't
#      verify against the public key compiled into them.
#   4. Code signing (optional, local): make sign, then re-upload the signed
#      artifacts to the release (code signing changes the bytes AND the sums,
#      so `make sign` re-signs SHA256SUMS too).
# NOTE: GitHub release assets are only anonymously downloadable when the repo
# is PUBLIC. Private repo = host dist/* on a public bucket instead and point
# GATE_BRIDGE_DOWNLOAD_BASE there.
# Manual fallback: make release VERSION=x.y.z && upload dist/* yourself.
release: clean winres
	GOOS=windows GOARCH=amd64 go build -ldflags "$(WIN_LDFLAGS)" -o dist/unitrise-gate-windows-amd64.exe ./cmd/unitrise-gate
	cp scripts/install.ps1 scripts/uninstall.ps1 dist/
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-darwin-arm64 ./cmd/unitrise-gate
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-darwin-amd64 ./cmd/unitrise-gate
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-linux-amd64 ./cmd/unitrise-gate
	cd dist && shasum -a 256 unitrise-gate-* *.ps1 > SHA256SUMS
	$(MAKE) sign-sums VERSION=$(VERSION)
	@ls -lh dist/

# Sign dist/SHA256SUMS -> dist/SHA256SUMS.sig with the release private key
# (RELEASE_SIGNING_KEY = path to it; never commit the key). The tool refuses a
# key that doesn't match the committed release-signing.pub.
sign-sums:
ifdef RELEASE_SIGNING_KEY
	go run ./tools/sign-release -key "$(RELEASE_SIGNING_KEY)" -version $(VERSION) -pub release-signing.pub dist/SHA256SUMS
else
	@echo "WARNING: RELEASE_SIGNING_KEY is not set - dist/SHA256SUMS is UNSIGNED and every agent will refuse to self-update to this release."
	@rm -f dist/SHA256SUMS.sig
endif

# Code signing hooks - both are OPTIONAL and no-op unless configured.
#   Windows Authenticode: set SIGNTOOL (path to signtool.exe or osslsigncode
#     wrapper script) - needs a purchased code-signing cert; unsigned builds
#     trip SmartScreen on first run (site IT clicks through "More info").
#   macOS: set CODESIGN_IDENTITY ("Developer ID Application: ...").
# Code signing changes the bytes, so this re-writes SHA256SUMS and re-signs it
# (pass RELEASE_SIGNING_KEY again).
sign:
ifdef SIGNTOOL
	$(SIGNTOOL) dist/unitrise-gate-windows-amd64.exe
endif
ifdef CODESIGN_IDENTITY
	codesign --force --options runtime -s "$(CODESIGN_IDENTITY)" dist/unitrise-gate-darwin-arm64
	codesign --force --options runtime -s "$(CODESIGN_IDENTITY)" dist/unitrise-gate-darwin-amd64
endif
	cd dist && shasum -a 256 unitrise-gate-* *.ps1 > SHA256SUMS
	$(MAKE) sign-sums VERSION=$(VERSION)

test:
	go vet ./...
	go test ./...

clean:
	rm -rf dist && mkdir -p dist
