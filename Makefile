# bv Makefile
#
# Build with SQLite FTS5 (full-text search) support enabled

.PHONY: build install clean test release-gate release-package release-verify release-audit

# Enable FTS5 for full-text search in SQLite exports
export CGO_CFLAGS := -DSQLITE_ENABLE_FTS5

build:
	go build -o bv ./cmd/bv

install:
	go install ./cmd/bv

clean:
	rm -f bv
	go clean

test:
	go test ./...

# These targets let RCH dispatch the complete release workflow as a Make build.
# The release gate owns toolchain settings; do not inherit the legacy FTS5 flag.
release-gate:
	env -u CGO_CFLAGS bash scripts/release_gate.sh run

release-package:
	env -u CGO_CFLAGS bash scripts/release_gate.sh package

release-verify:
	env -u CGO_CFLAGS bash scripts/release_gate.sh verify

# Keep the audit tool outside the application's module requirements.
release-audit:
	env -u CGO_CFLAGS go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
