#!/usr/bin/env bash
set -euo pipefail

REPO_OWNER="Dicklesworthstone"
REPO_NAME="beads_viewer"
BIN_NAME="bv"

TMP_DIRS=()

cleanup_tmp_dirs() {
    local dir
    # Use ${array[@]+"${array[@]}"} pattern to avoid unbound variable error
    # when TMP_DIRS is empty with set -u enabled
    for dir in ${TMP_DIRS[@]+"${TMP_DIRS[@]}"}; do
        [ -n "$dir" ] && rm -rf "$dir"
    done
}

make_tmp_dir() {
    local dir
    dir=$(mktemp -d)
    TMP_DIRS+=("$dir")
    printf '%s\n' "$dir"
}

trap cleanup_tmp_dirs EXIT

default_install_dir() {
    if [ -n "${INSTALL_DIR:-}" ]; then
        echo "$INSTALL_DIR"
        return
    fi

    # Prefer user-local bin directory to avoid requiring root privileges
    local user_local_bin="${HOME}/.local/bin"
    if [ -d "$user_local_bin" ] && [ -w "$user_local_bin" ]; then
        echo "$user_local_bin"
        return
    fi

    # Try writable Homebrew/standard prefixes on macOS
    for dir in /usr/local/bin /opt/homebrew/bin /opt/local/bin; do
        if [ -d "$dir" ] && [ -w "$dir" ]; then
            echo "$dir"
            return
        fi
    done

    # Fall back to the first writable entry in PATH
    IFS=: read -r -a path_entries <<<"${PATH:-}"
    for dir in "${path_entries[@]}"; do
        if [ -d "$dir" ] && [ -w "$dir" ]; then
            echo "$dir"
            return
        fi
    done

    # Last resort: use ~/.local/bin (will be created if needed)
    # This avoids requiring root privileges by default
    echo "$user_local_bin"
}

is_dir_in_path() {
    local dir="$1"
    case ":${PATH}:" in
        *":${dir}:"*) return 0 ;;
        *) return 1 ;;
    esac
}

check_path_and_warn() {
    local dir="$1"
    if ! is_dir_in_path "$dir"; then
        print_warn "$dir is not in your PATH"
        echo ""
        echo "To add it, add this line to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
        echo ""
        echo "  export PATH=\"$dir:\$PATH\""
        echo ""
    fi
}

INSTALL_DIR="$(default_install_dir)"

print_info() { printf "\033[1;34m==>\033[0m %s\n" "$1"; }
print_success() { printf "\033[1;32m==>\033[0m %s\n" "$1"; }
print_error() { printf "\033[1;31m==>\033[0m %s\n" "$1"; }
print_warn() { printf "\033[1;33m==>\033[0m %s\n" "$1"; }

detect_platform() {
    local os arch

    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    case "$os" in
        linux) os="linux" ;;
        darwin) os="darwin" ;;
        mingw*|msys*|cygwin*) os="windows" ;;
        *) print_error "Unsupported OS: $os"; return 1 ;;
    esac

    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        arm64|aarch64) arch="arm64" ;;
        *) print_error "Unsupported architecture: $arch"; return 1 ;;
    esac

    echo "${os}_${arch}"
}

get_latest_release() {
    local url="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/latest"

    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url" 2>/dev/null || return 1
    elif command -v wget >/dev/null 2>&1; then
        wget -qO- "$url" 2>/dev/null || return 1
    else
        return 1
    fi
}

download_file() {
    local url="$1"
    local dest="$2"

    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url" -o "$dest" || return 1
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$url" -O "$dest" || return 1
    else
        return 1
    fi
}

ensure_install_dir() {
    local dir="$1"

    if [ -d "$dir" ]; then
        return 0
    fi

    if mkdir -p "$dir" 2>/dev/null; then
        return 0
    fi

    print_info "Creating $dir requires sudo..."
    sudo mkdir -p "$dir"
}

PYTHON_CMD=""

ensure_python() {
    if [ -n "$PYTHON_CMD" ]; then
        return 0
    fi

    if command -v python3 >/dev/null 2>&1; then
        PYTHON_CMD="$(command -v python3)"
        return 0
    fi

    if command -v python >/dev/null 2>&1; then
        PYTHON_CMD="$(command -v python)"
        return 0
    fi

    print_error "Python 3 is required to parse GitHub release metadata."
    print_error "Please install python3 (e.g., 'xcode-select --install' on macOS) or install jq."
    return 1
}

fetch_latest_go_pkg() {
    # Emits: version<newline>url (for macOS .pkg matching current arch)
    ensure_python || return 1

    local arch
    arch="$(uname -m)"
    case "$arch" in
        arm64|aarch64) arch="arm64" ;;
        x86_64|amd64) arch="amd64" ;;
        *) print_error "Unsupported macOS architecture for Go install: $arch"; return 1 ;;
    esac

    "$PYTHON_CMD" - "$arch" <<'PY'
import json
import sys
import urllib.request


def main() -> int:
    arch = sys.argv[1]
    try:
        with urllib.request.urlopen("https://go.dev/dl/?mode=json") as resp:
            data = json.load(resp)
    except Exception as exc:  # noqa: BLE001
        sys.stderr.write(f"Failed to fetch Go releases: {exc}\n")
        return 1

    release = next((r for r in data if r.get("stable")), None)
    if not release:
        return 1

    version = release.get("version") or ""
    files = release.get("files") or []
    pkg = next(
        (f for f in files if f.get("os") == "darwin" and f.get("arch") == arch and f.get("filename", "").endswith(".pkg")),
        None,
    )

    if not pkg:
        return 1

    url = pkg.get("url") or ""
    print(version)
    print(url)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
PY
}

install_go_from_pkg() {
    local version url tmpdir pkg_path

    read -r version url < <(fetch_latest_go_pkg) || return 1

    if [ -z "$version" ] || [ -z "$url" ]; then
        return 1
    fi

    print_info "Downloading Go $version (.pkg)..."
    tmpdir=$(mktemp -d)
    pkg_path="$tmpdir/go.pkg"

    if ! curl -fsSL "$url" -o "$pkg_path"; then
        print_error "Failed to download Go installer from $url"
        rm -rf "$tmpdir"
        return 1
    fi

    print_info "Installing Go $version (requires sudo)..."
    if sudo installer -pkg "$pkg_path" -target / >/dev/null; then
        print_success "Installed Go $version"
        rm -rf "$tmpdir"
        return 0
    fi

    print_error "Go installer failed"
    rm -rf "$tmpdir"
    return 1
}

version_ge() {
    # Returns 0 if $1 >= $2 (both are dot-separated numeric strings)
    local IFS=.
    local i ver1=($1) ver2=($2)
    for ((i=0; i<${#ver1[@]} || i<${#ver2[@]}; i++)); do
        local v1=${ver1[i]:-0}
        local v2=${ver2[i]:-0}
        if ((10#$v1 > 10#$v2)); then return 0; fi
        if ((10#$v1 < 10#$v2)); then return 1; fi
    done
    return 0
}

select_release_asset() {
    local platform="$1"
    ensure_python || return 1

    local release_json
    release_json=$(cat) || return 1

    BV_RELEASE_JSON="$release_json" "$PYTHON_CMD" - "$platform" "$BIN_NAME" <<'PY'
import json
import os
import sys


def pick_asset(data, platform, bin_name):
    ext = ".zip" if platform.startswith("windows_") else ".tar.gz"
    assets = data.get("assets") or []

    # Prefer exact platform match with expected ext
    for asset in assets:
        name = asset.get("name") or ""
        if platform in name and name.endswith(ext):
            url = asset.get("browser_download_url") or ""
            if url:
                return name, url

    # Fallback: any asset that contains platform and correct ext
    for asset in assets:
        name = asset.get("name") or ""
        url = asset.get("browser_download_url") or ""
        if platform.replace("_", "") in name.replace("_", "") and name.endswith(ext) and url:
            return name, url

    return None, None


def main():
    if len(sys.argv) < 3:
        return 1
    platform = sys.argv[1]
    bin_name = sys.argv[2]
    release_json = os.environ.get("BV_RELEASE_JSON", "")
    if not release_json:
        sys.stderr.write("Missing release metadata\n")
        return 1
    try:
        data = json.loads(release_json)
    except Exception as exc:  # noqa: BLE001
        sys.stderr.write(f"Failed to parse release JSON: {exc}\n")
        return 1

    version = data.get("tag_name") or ""
    name, url = pick_asset(data, platform, bin_name)

    print(version)
    print(url or "")
    print(name or "")

    return 0 if url else 1


if __name__ == "__main__":
    raise SystemExit(main())
PY
}

is_tty() {
    [ -t 0 ] && [ -t 1 ]
}

ensure_go() {
    local min_version="1.21"
    local go_version=""

    if command -v go >/dev/null 2>&1; then
        go_version=$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')
        if version_ge "$go_version" "$min_version"; then
            printf '%s' "$go_version"
            return 0
        fi
        print_warn "Go $min_version or later is required. Found: go$go_version"
    else
        print_warn "Go is not installed."
    fi

    # Try to install/upgrade via Homebrew on macOS
    if command -v brew >/dev/null 2>&1; then
        if is_tty; then
            printf "Install/upgrade Go via Homebrew now? [Y/n] "
            read -r reply
            if [[ "$reply" =~ ^[Nn] ]]; then
                return 1
            fi
        else
            print_info "Attempting non-interactive install of Go via Homebrew..."
        fi

        if brew install go || brew upgrade go; then
            go_version=$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')
            if version_ge "$go_version" "$min_version"; then
                print_success "Installed Go $go_version via Homebrew"
                printf '%s' "$go_version"
                return 0
            fi
        else
            print_error "Homebrew installation of Go failed."
        fi
    else
        print_warn "Homebrew not found."
    fi

    # Fallback: download official macOS pkg directly
    if [ "$(uname -s)" = "Darwin" ]; then
        if is_tty; then
            printf "Download and install the latest Go from go.dev now? [Y/n] "
            read -r reply
            if [[ "$reply" =~ ^[Nn] ]]; then
                return 1
            fi
        else
            print_info "Attempting non-interactive Go install via official pkg..."
        fi

        if install_go_from_pkg; then
            # Try common locations before giving up (pkg installs to /usr/local/go/bin)
            local candidates=( "go" "/usr/local/go/bin/go" "/usr/local/bin/go" )
            for candidate in "${candidates[@]}"; do
                if command -v "$candidate" >/dev/null 2>&1; then
                    go_version=$("$candidate" version 2>/dev/null | awk '{print $3}' | sed 's/^go//')
                    if [ -n "$go_version" ] && version_ge "$go_version" "$min_version"; then
                        print_success "Detected Go $go_version after pkg install"
                        printf '%s' "$go_version"
                        return 0
                    fi
                fi
            done
        fi
    fi

    return 1
}

try_binary_install() {
    local platform="$1"
    local tmp_dir

    print_info "Checking for pre-built binary..."

    # Get latest release info
    local release_json
    release_json=$(get_latest_release) || return 1

    local parsed version download_url asset_name
    parsed=$(printf '%s' "$release_json" | select_release_asset "$platform") || true

    version=$(printf '%s' "$parsed" | sed -n '1p')
    download_url=$(printf '%s' "$parsed" | sed -n '2p')
    asset_name=$(printf '%s' "$parsed" | sed -n '3p')

    if [ -z "$download_url" ]; then
        print_warn "No pre-built binary found for $platform"
        return 1
    fi

    if [ -z "$version" ]; then
        version="unknown"
    fi

    print_info "Latest release: $version"
    if [ -n "$asset_name" ]; then
        print_info "Selected asset: $asset_name"
    fi

    print_info "Downloading $download_url..."

    tmp_dir=$(make_tmp_dir)

    local ext=".tar.gz"
    if [[ "$download_url" == *.zip ]]; then
        ext=".zip"
    fi

    local archive_path="$tmp_dir/archive${ext}"

    if ! download_file "$download_url" "$archive_path"; then
        print_warn "Download failed"
        return 1
    fi

    # Verify the archive against the release's published checksums.txt
    # (produced by goreleaser). Fail closed: no checksum, no install.
    if [ -z "$asset_name" ]; then
        asset_name="${download_url##*/}"
    fi
    local checksums_url="${download_url%/*}/checksums.txt"
    local checksums_path="$tmp_dir/checksums.txt"
    # Note: these paths exit (not return) so a verification failure can never
    # silently fall back to the unverified build-from-main path.
    if ! download_file "$checksums_url" "$checksums_path"; then
        print_error "Could not download checksums.txt from the release; refusing unverified install"
        exit 1
    fi
    local expected_sha actual_sha
    expected_sha=$(awk -v n="$asset_name" '$2 == n || $2 == ("*" n) {print $1; exit}' "$checksums_path")
    if [ -z "$expected_sha" ]; then
        print_error "checksums.txt does not list $asset_name; refusing to install"
        exit 1
    fi
    if command -v sha256sum >/dev/null 2>&1; then
        actual_sha=$(sha256sum "$archive_path" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        actual_sha=$(shasum -a 256 "$archive_path" | awk '{print $1}')
    else
        print_error "No SHA-256 tool (sha256sum/shasum) available; refusing unverified install"
        exit 1
    fi
    if [ "$expected_sha" != "$actual_sha" ]; then
        print_error "SHA-256 mismatch for $asset_name: expected $expected_sha, got $actual_sha"
        exit 1
    fi
    print_info "SHA-256 verified against release checksums.txt"

    # Extract the binary
    print_info "Extracting..."

    if [[ "$ext" == ".zip" ]]; then
        if command -v unzip >/dev/null 2>&1; then
            if ! unzip -q "$archive_path" -d "$tmp_dir"; then
                print_error "Release archive could not be extracted; existing installation was not changed"
                exit 1
            fi
        else
            print_warn "unzip not found"
            return 1
        fi
    else
        if ! tar -xzf "$archive_path" -C "$tmp_dir"; then
            print_error "Release archive could not be extracted; existing installation was not changed"
            exit 1
        fi
    fi

    # Find the binary in extracted contents
    local binary_path
    binary_path=$(find "$tmp_dir" -type f -name "$BIN_NAME" -perm -111 2>/dev/null | head -1)

    if [ -z "$binary_path" ]; then
        # Try without executable check (for freshly extracted files)
        binary_path=$(find "$tmp_dir" -type f -name "$BIN_NAME" 2>/dev/null | head -1)
    fi

    if [ -z "$binary_path" ] && [[ "$platform" == windows_* ]]; then
        binary_path=$(find "$tmp_dir" -type f -name "${BIN_NAME}.exe" 2>/dev/null | head -1)
    fi

    if [ -z "$binary_path" ]; then
        print_error "Binary not found in release archive; existing installation was not changed"
        exit 1
    fi

    chmod +x "$binary_path"

    # A valid archive checksum does not prove it contains the requested release.
    # Verify before replacing a working installation, and never fall back to a
    # source build after an identity failure.
    local reported reported_version
    if ! reported=$("$binary_path" --version 2>&1); then
        print_error "Downloaded binary could not report its version: $reported"
        exit 1
    fi
    if [[ "$reported" =~ ^bv[[:space:]]+(v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?)$ ]]; then
        reported_version="${BASH_REMATCH[1]}"
    else
        print_error "Unexpected --version output from downloaded binary: $reported"
        exit 1
    fi
    if [ "${reported_version#v}" != "${version#v}" ]; then
        print_error "Downloaded binary reports $reported_version, expected $version; existing installation was not changed"
        exit 1
    fi

    # Install to destination
    ensure_install_dir "$INSTALL_DIR"
    local dest_path="$INSTALL_DIR/$BIN_NAME"

    if [ -w "$INSTALL_DIR" ]; then
        mv "$binary_path" "$dest_path"
    else
        print_info "Installing to $INSTALL_DIR requires sudo..."
        sudo mv "$binary_path" "$dest_path"
    fi

    print_success "Installed $BIN_NAME $version to $dest_path"
    return 0
}

try_go_install() {
    print_info "Attempting to build from source with go build..."

    local go_version
    if ! go_version=$(ensure_go); then
        print_error "Go 1.21 or later is required for building from source."
        exit 1
    fi

    print_info "Using Go $go_version"

    local tmp_dir src_dir repo_url tarball_url tarball_path build_output fetched=0
    tmp_dir=$(make_tmp_dir)
    src_dir="$tmp_dir/src"
    repo_url="https://github.com/${REPO_OWNER}/${REPO_NAME}.git"

    print_info "Fetching source..."

    if command -v git >/dev/null 2>&1; then
        if git clone --depth 1 "$repo_url" "$src_dir" >/dev/null 2>&1; then
            fetched=1
        else
            print_warn "git clone failed, attempting tarball download..."
        fi
    fi

    if [ "$fetched" -ne 1 ]; then
        tarball_url="https://codeload.github.com/${REPO_OWNER}/${REPO_NAME}/tar.gz/refs/heads/main"
        tarball_path="$tmp_dir/source.tar.gz"
        if ! download_file "$tarball_url" "$tarball_path"; then
            print_error "Failed to download source tarball from GitHub."
            exit 1
        fi
        tar -xzf "$tarball_path" -C "$tmp_dir"
        src_dir=$(find "$tmp_dir" -maxdepth 1 -type d -name "${REPO_NAME}-*" | head -1)
        if [ -z "$src_dir" ]; then
            print_error "Could not locate extracted source directory."
            exit 1
        fi
    fi

    print_info "Building $BIN_NAME from source..."
    build_output="$tmp_dir/$BIN_NAME"
    if ! (cd "$src_dir" && GO111MODULE=on CGO_ENABLED=0 go build -o "$build_output" "./cmd/$BIN_NAME"); then
        print_error "Go build failed."
        exit 1
    fi

    ensure_install_dir "$INSTALL_DIR"
    local dest_path="$INSTALL_DIR/$BIN_NAME"

    if [ -w "$INSTALL_DIR" ]; then
        mv "$build_output" "$dest_path"
    else
        print_info "Installing to $INSTALL_DIR requires sudo..."
        sudo mv "$build_output" "$dest_path"
    fi

    print_success "Built and installed $BIN_NAME from source to $dest_path"
    return 0
}

main() {
    print_info "Installing $BIN_NAME..."

    local platform
    platform=$(detect_platform) || {
        print_warn "Could not detect platform, will try building from source"
        try_go_install
        check_path_and_warn "$INSTALL_DIR"
        exit 0
    }

    print_info "Detected platform: $platform"

    # First, try to download pre-built binary
    if try_binary_install "$platform"; then
        check_path_and_warn "$INSTALL_DIR"
        print_info "Run '$BIN_NAME' in any beads project to view issues."
        echo ""
        echo "Tip: You can also install via Homebrew:"
        echo "  brew install dicklesworthstone/tap/bv"
        exit 0
    fi

    # Fall back to building from source
    print_info "Pre-built binary not available, falling back to source build..."
    try_go_install

    check_path_and_warn "$INSTALL_DIR"
    print_info "Run '$BIN_NAME' in any beads project to view issues."
    echo ""
    echo "Tip: You can also install via Homebrew:"
    echo "  brew install dicklesworthstone/tap/bv"
}

if [[ ${BASH_SOURCE+x} != x ]]; then
    main "$@"
elif [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
