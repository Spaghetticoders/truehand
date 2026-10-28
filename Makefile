# `make app` builds the web app and embeds it in the binary; plain `go build` leaves it out.
# `make dist` cross-builds the binaries the Mac installs on remote machines (no web app:
# a peer serves only its unix socket).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -X main.version=$(VERSION)
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

.PHONY: app web dist e2e shell test

app: web
	go build -tags webui -ldflags "$(LDFLAGS)" -o bin/truehand ./cmd/truehand

web:
	cd web && bun install --frozen-lockfile && bun run build

dist:
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "bin/truehand-$$os-$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o bin/truehand-$$os-$$arch ./cmd/truehand || exit 1; \
	done

# For browser tests driven from agent sessions only: no agent guard, fake OS authentication,
# runs only with HOME under /tmp or $TMPDIR. Never shipped.
e2e: web
	go build -tags "webui truehand_e2e" -ldflags "$(LDFLAGS)" -o bin/truehand-e2e ./cmd/truehand

# The Mac app (Tauri) with this binary as its sidecar.
shell: app
	cd shell && bun install --frozen-lockfile && bun run build

test:
	go vet ./...
	go test ./...
