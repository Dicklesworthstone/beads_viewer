.PHONY: build build-candidate build-release test test-release test-reproducible clean size check fmt clippy

# Development build (faster, larger)
build:
	wasm-pack build --target web --dev

# Generate an external candidate before updating the paired shipped assets.
build-candidate:
	../scripts/build_graph_wasm.sh --build-only

# Release verification uses the same pinned external build as the release gate.
build-release:
	../scripts/build_graph_wasm.sh

# Run Rust unit tests (not WASM)
test:
	cargo test

# Exercise both Rust components with their actual release optimization profiles.
# Invoking from this directory uses the graph crate's dated compiler pin.
test-release:
	cargo test --locked --release --all-features
	cargo test --manifest-path ../pkg/export/wasm_scorer/Cargo.toml --locked --release

# Rebuild with two physical compiler homes and execute the graph fixtures.
test-reproducible:
	../tests/scripts/graph_wasm_test.sh

# Run WASM tests in headless browser
test-wasm:
	wasm-pack test --headless --firefox

# Clean build artifacts
clean:
	cargo clean
	rm -rf pkg/

# Check size
size: build-release
	@echo "WASM size:"
	@wc -c ../pkg/export/viewer_assets/vendor/bv_graph_bg.wasm
	@echo "Gzipped:"
	@gzip -c ../pkg/export/viewer_assets/vendor/bv_graph_bg.wasm | wc -c

# Format code
fmt:
	cargo fmt

# Run clippy lints
clippy:
	cargo clippy -- -D warnings

# Check without building
check:
	cargo check
