# Copyright 2024 The Sqlite Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

.PHONY:	all build_all_targets clean edit editor licenses sbom test test_pcache vendor vendor-check work

# Pinned deduplicator. undup folds byte-identical declarations shared across the
# per-target generated files in lib/ and vec/ into build-tagged shared files,
# keeping each tag's module download under Go's 500MB cap. Bump deliberately.
UNDUP = modernc.org/undup@v0.0.5

# Extra flags for the vendor tool's -preflight and -stamp steps. The one there
# is: make vendor VENDORFLAGS=-allow-dirty vendors from a sibling checkout with
# uncommitted changes, such as a debug build of libsqlite3 (see doc.go), and
# records that in vendor.json. The recipe's last step then fails, as does
# TestVendorStamp, so such a tree is never committed or released.
VENDORFLAGS =

all: editor
	golint 2>&1
	staticcheck 2>&1

build_all_targets:
	GOOS=darwin GOARCH=amd64 go test -c -o /dev/null
	GOOS=darwin GOARCH=amd64 go build -v ./...
	GOOS=darwin GOARCH=arm64 go test -c -o /dev/null
	GOOS=darwin GOARCH=arm64 go build -v ./...
	GOOS=freebsd GOARCH=amd64 go test -c -o /dev/null
	GOOS=freebsd GOARCH=amd64 go build -v ./...
	GOOS=freebsd GOARCH=386 go test -c -o /dev/null
	GOOS=freebsd GOARCH=386 go build -v ./...
	GOOS=freebsd GOARCH=arm go test -c -o /dev/null
	GOOS=freebsd GOARCH=arm go build -v ./...
	GOOS=freebsd GOARCH=arm64 go test -c -o /dev/null
	GOOS=freebsd GOARCH=arm64 go build -v ./...
	GOOS=illumos GOARCH=amd64 go test -c -o /dev/null
	GOOS=illumos GOARCH=amd64 go build -v ./...
	GOOS=linux GOARCH=386 go test -c -o /dev/null
	GOOS=linux GOARCH=386 go build -v ./...
	GOOS=linux GOARCH=amd64 go test -c -o /dev/null
	GOOS=linux GOARCH=amd64 go build -v ./...
	GOOS=linux GOARCH=arm go test -c -o /dev/null
	GOOS=linux GOARCH=arm go build -v ./...
	GOOS=linux GOARCH=arm64 go test -c -o /dev/null
	GOOS=linux GOARCH=arm64 go build -v ./...
	GOOS=linux GOARCH=loong64 go test -c -o /dev/null
	GOOS=linux GOARCH=loong64 go build -v ./...
	GOOS=linux GOARCH=ppc64le go test -c -o /dev/null
	GOOS=linux GOARCH=ppc64le go build -v ./...
	GOOS=linux GOARCH=riscv64 go test -c -o /dev/null
	GOOS=linux GOARCH=riscv64 go build -v ./...
	GOOS=linux GOARCH=s390x go test -c -o /dev/null
	GOOS=linux GOARCH=s390x go build -v ./...
	GOOS=netbsd GOARCH=amd64 go test -c -o /dev/null
	GOOS=netbsd GOARCH=amd64 go build -v ./...
	GOOS=openbsd GOARCH=amd64 go test -c -o /dev/null
	GOOS=openbsd GOARCH=amd64 go build -v ./...
	GOOS=openbsd GOARCH=arm64 go test -c -o /dev/null
	GOOS=openbsd GOARCH=arm64 go build -v ./...
	GOOS=windows GOARCH=386 go test -c -o /dev/null
	GOOS=windows GOARCH=386 go build -v ./...
	GOOS=windows GOARCH=amd64 go test -c -o /dev/null
	GOOS=windows GOARCH=amd64 go build -v ./...
	GOOS=windows GOARCH=arm64 go test -c -o /dev/null
	GOOS=windows GOARCH=arm64 go build -v ./...
	echo done

clean:
	rm -f log-* cpu.test mem.test *.out go.work* licgen attest
	go clean

edit:
	@if [ -f "Session.vim" ]; then gvim -S & else gvim -p Makefile go.mod builder.json all_test.go & fi

editor:
	go test -c -o /dev/null
	go build -v  -o /dev/null ./...
	cd vendor_libs && go build -o /dev/null main.go stamp.go
	cd licensegen && go build -tags none -o /dev/null .
	cd attestgen && go build -tags none -o /dev/null .

# Regenerate LICENSE-3RD-PARTY.md, SBOM.md and the two machine-readable SBOMs
# from the module graph and the vendored C. Run after any dependency bump or
# re-vendoring. `./licgen -check` writes nothing and fails if any of the four
# committed files is stale; that is the form for CI.
licenses: sbom

sbom:
	cd licensegen && go build -tags none -o ../licgen .
	./licgen
	rm -f licgen

test:
	go test -v -timeout 24h

# The whole suite through the pluggable page cache binding, with
# modernc.org/sqlite/pcache registered. See pcachepool_test.go.
test_pcache:
	go test -v -timeout 24h -tags pcachepool
	
# The steps make vendor and make vendor-check share. vendor_preflight builds
# the tool and refuses unusable sibling checkouts; vendor_copy copies lib/ and
# vec/ from them, folds and formats.
define vendor_preflight
cd vendor_libs && go build -o ../vendor main.go stamp.go
# Before anything is touched: refuse a dirty checkout, two checkouts on
# different libc versions, or a libsqlite_vec built against another
# libsqlite3 than ../libsqlite3, and remember what was seen. See stamp.go.
./vendor -preflight -undup=$(UNDUP) $(VENDORFLAGS)
endef

define vendor_copy
# ../libsqlite3 and ../libsqlite_vec are read one full per-target file at a
# time. They ship expanded today, but either may adopt the deduplicated
# layout (modernc.org/builder's NW autogen); the tool detects that and
# expands a temporary copy, leaving those checkouts untouched. Hence the pin:
# one version of record for this repo, wherever undup is invoked.
./vendor -undup=$(UNDUP)
# Fold byte-identical declarations back into build-tagged shared files. undup
# only touches files carrying the generated-code marker, never hand-written
# platform files (libsqlite3_*.go, hooks_*.go, ...).
go run $(UNDUP) -dir lib
go run $(UNDUP) -dir vec
"$$(go env GOROOT)/bin/gofmt" -s -w lib/sqlite*.go vec/vec*.go
endef

# The whole recipe runs on one Go toolchain, the one vendor.json records: gofmt
# output differs between Go releases, so the toolchain is one of the inputs.
# GOTOOLCHAIN=local makes a toolchain older than go.mod asks for an error
# rather than a silent download for some steps and not others.
vendor: export GOTOOLCHAIN := local
vendor:
	$(vendor_preflight)
	# Reconstruct full per-target files (a no-op the first time), so the freshly
	# vendored transpiles overwrite a clean tree with no stale shared files.
	go run $(UNDUP) -expand -dir lib
	go run $(UNDUP) -expand -dir vec
	$(vendor_copy)
	make build_all_targets
	# Last, once everything above has succeeded: record the sources, the
	# toolchain and a digest of the output in vendor.json, then check it.
	./vendor -stamp -undup=$(UNDUP) $(VENDORFLAGS)
	rm -f vendor
	go test ./internal/vendorstamp/

# Vendors again from ../libsqlite3 and ../libsqlite_vec, without the
# cross-builds, and fails unless every file vendor.json covers comes out byte
# for byte as committed and vendor.json itself is the same apart from the Go
# release it records. With the siblings checked out at the commits vendor.json
# names, as the attest job in .gitlab-ci.yml does, the stamp is reproduced
# rather than taken on trust. The files are deleted before the copy, which
# leaves nothing for undup -expand to do: all of them are generated, so one
# the recipe no longer writes shows up as deleted. On success the committed
# vendor.json is checked out again, so the tree is left as committed and not
# stamped with the Go that ran here.
vendor-check: export GOTOOLCHAIN := local
vendor-check:
	$(vendor_preflight)
	rm -f lib/sqlite*.go vec/vec*.go LICENSE-SQLITE_VEC
	$(vendor_copy)
	./vendor -stamp -undup=$(UNDUP) $(VENDORFLAGS)
	rm -f vendor
	git diff --exit-code --stat -I '^ *"go": ' -- vendor.json lib vec LICENSE-SQLITE_VEC
	test -z "$$(git status --porcelain --untracked-files=all --ignored -- lib vec LICENSE-SQLITE_VEC)"
	git checkout -q -- vendor.json

work:
	rm -f go.work*
	go work init
	go work use .
	go work use ../cc/v4
	go work use ../ccgo/v3
	go work use ../ccgo/v4
	go work use ../libc
	go work use ../libtcl8.6
	go work use ../libsqlite3
	go work use ../libz
