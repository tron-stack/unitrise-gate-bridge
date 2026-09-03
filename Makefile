VERSION ?= 0.4.0
LDFLAGS := -s -w -X github.com/mytruckyards/unitrise-gate-bridge/internal/api.AgentVersion=$(VERSION)

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

# Release checklist:
#   1. make release VERSION=x.y.z         (builds all platforms + SHA256SUMS)
#   2. make sign                          (optional; needs the env vars below)
#   3. upload dist/* to the hosting bucket/release
#   4. set GATE_AGENT_LATEST_VERSION=x.y.z (+ GATE_BRIDGE_DOWNLOAD_BASE) on the
#      backend - update-check, the console chip, and `unitrise-gate update`
#      all key off those two env vars.
release: clean winres
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-windows-amd64.exe ./cmd/unitrise-gate
	cp scripts/install.ps1 scripts/uninstall.ps1 dist/
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-darwin-arm64 ./cmd/unitrise-gate
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-darwin-amd64 ./cmd/unitrise-gate
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-linux-amd64 ./cmd/unitrise-gate
	cd dist && shasum -a 256 unitrise-gate-* *.ps1 > SHA256SUMS
	@ls -lh dist/

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
