#!/bin/bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WASM_SRC="$ROOT_DIR/pkg/export/wasm_scorer"
WASM_OUT="${BV_HYBRID_WASM_DIR:-$ROOT_DIR/.bv/wasm/hybrid}"
OUT_DIR_SET=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --out-dir)
      if [ "$#" -lt 2 ]; then
        echo "missing argument for --out-dir" >&2
        exit 1
      fi
      WASM_OUT="$2"
      OUT_DIR_SET=1
      shift 2
      ;;
    --out-dir=*)
      WASM_OUT="${1#--out-dir=}"
      OUT_DIR_SET=1
      shift
      ;;
    -*)
      echo "unknown flag: $1" >&2
      exit 1
      ;;
    *)
      if [ "$OUT_DIR_SET" -eq 0 ]; then
        WASM_OUT="$1"
        OUT_DIR_SET=1
        shift
      else
        echo "unexpected argument: $1" >&2
        exit 1
      fi
      ;;
  esac
done

if ! command -v wasm-pack >/dev/null 2>&1; then
  echo "wasm-pack not found. Install with: cargo install wasm-pack" >&2
  exit 1
fi

if [ ! -d "$WASM_SRC" ]; then
  echo "WASM source directory not found: $WASM_SRC" >&2
  exit 1
fi

mkdir -p "$WASM_OUT"

(cd "$WASM_SRC" && wasm-pack build --release --target web --out-dir "$WASM_OUT")

echo "Hybrid WASM assets built to $WASM_OUT"
