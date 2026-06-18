# phootball developer tasks. `make` (or `make all`) runs the full gate the CI runs.
.PHONY: all build test test-race vet headless golden tidy

all: vet build test test-race headless golden

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

# headless: the authoritative server must NEVER link Ebiten (it runs the sim only, so it can
# run on a graphics-less box). This guard fails the build if it ever does.
headless:
	@go list -deps ./cmd/server | grep -q ebiten && { echo "FAIL: cmd/server links ebiten"; exit 1; } || echo "ok: cmd/server is headless"

# golden: the feel-freeze replay characterization. Regenerate with: go test ./internal/sim -run TestGoldenReplay -update
golden:
	go test ./internal/sim -run TestGoldenReplay

tidy:
	go mod tidy
