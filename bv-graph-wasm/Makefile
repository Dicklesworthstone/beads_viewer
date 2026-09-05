.PHONY: build build-release test clean size check fmt clippy

# Development build (faster, larger)
build:
	wasm-pack build --target web --dev

# Release verification uses the same pinned external build as the release gate.
build-release:
	../scripts/build_graph_wasm.sh

# Run Rust unit tests (not WASM)
test:
	cargo test

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
