VERSION ?= 0.1.0
LDFLAGS := -s -w -X github.com/mytruckyards/unitrise-gate-bridge/internal/api.AgentVersion=$(VERSION)

.PHONY: build release test clean

build:
	go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate ./cmd/unitrise-gate

release: clean
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-windows-amd64.exe ./cmd/unitrise-gate
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-darwin-arm64 ./cmd/unitrise-gate
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-darwin-amd64 ./cmd/unitrise-gate
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/unitrise-gate-linux-amd64 ./cmd/unitrise-gate
	@ls -lh dist/

test:
	go vet ./...
	go test ./...

clean:
	rm -rf dist && mkdir -p dist
