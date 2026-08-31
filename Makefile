VERSION ?= 0.3.0
LDFLAGS := -s -w -X github.com/mytruckyards/unitrise-gate-bridge/internal/api.AgentVersion=$(VERSION)

.PHONY: build release sign test clean

build:
	go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate ./cmd/unitrise-gate

# Release checklist:
#   1. make release VERSION=x.y.z         (builds all platforms + SHA256SUMS)
#   2. make sign                          (optional; needs the env vars below)
#   3. upload dist/* to the hosting bucket/release
#   4. set GATE_AGENT_LATEST_VERSION=x.y.z (+ GATE_BRIDGE_DOWNLOAD_BASE) on the
#      backend - update-check, the console chip, and `unitrise-gate update`
#      all key off those two env vars.
release: clean
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-windows-amd64.exe ./cmd/unitrise-gate
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-darwin-arm64 ./cmd/unitrise-gate
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-darwin-amd64 ./cmd/unitrise-gate
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-linux-amd64 ./cmd/unitrise-gate
	cd dist && shasum -a 256 unitrise-gate-* > SHA256SUMS
	@ls -lh dist/

# Code signing hooks - both are OPTIONAL and no-op unless configured.
#   Windows Authenticode: set SIGNTOOL (path to signtool.exe or osslsigncode
#     wrapper script) - needs a purchased code-signing cert; unsigned builds
#     trip SmartScreen on first run (site IT clicks through "More info").
#   macOS: set CODESIGN_IDENTITY ("Developer ID Application: ...").
# Re-run `cd dist && shasum -a 256 unitrise-gate-* > SHA256SUMS` after signing
# (signing changes the bytes).
sign:
ifdef SIGNTOOL
	$(SIGNTOOL) dist/unitrise-gate-windows-amd64.exe
endif
ifdef CODESIGN_IDENTITY
	codesign --force --options runtime -s "$(CODESIGN_IDENTITY)" dist/unitrise-gate-darwin-arm64
	codesign --force --options runtime -s "$(CODESIGN_IDENTITY)" dist/unitrise-gate-darwin-amd64
endif
	cd dist && shasum -a 256 unitrise-gate-* > SHA256SUMS

test:
	go vet ./...
	go test ./...

clean:
	rm -rf dist && mkdir -p dist
