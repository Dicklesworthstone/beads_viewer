# bv Makefile
# Pure-Go build (modernc.org/sqlite includes FTS5 without CGO).

.PHONY: build install clean test release-gate release-package release-verify release-audit

build:
	go build -o bv ./cmd/bv

install:
	go install ./cmd/bv

clean:
	rm -f bv
	go clean

test:
	BV_NO_BROWSER=1 BV_TEST_MODE=1 go test ./...

# These targets let DSR dispatch the complete release workflow as a Make build.
release-gate:
	bash scripts/release_gate.sh run

release-package:
	bash scripts/release_gate.sh package

release-verify:
	bash scripts/release_gate.sh verify

# Keep the audit tool outside the application's module requirements.
release-audit:
	bash scripts/release_gate.sh audit
