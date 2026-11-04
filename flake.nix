{
  description = "bv - Terminal UI for the Beads issue tracker";

  inputs = {
    # Go 1.26 support while retaining the existing Intel macOS package.
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };

        # Keep the locked nixpkgs platform support (including Intel macOS),
        # but replace its Go 1.26.7 compiler with the security release. This is
        # the upstream go.dev source archive SHA-256, not a vendor/NAR hash.
        go = pkgs.go_1_26.overrideAttrs (_: {
          version = "1.26.10";
          src = pkgs.fetchurl {
            url = "https://go.dev/dl/go1.26.10.src.tar.gz";
            hash = "sha256-/WPog7ovK/ypPBZGsx6+9j4rHk0rMXyXidl2po2B828=";
          };
        });
        buildGoModule = pkgs.buildGo126Module.override { inherit go; };

        bvVersion = "0.26.1";

        # To update vendorHash after go.mod/go.sum changes:
        # 1. Set vendorHash to: pkgs.lib.fakeHash
        # 2. Run: nix build .#bv 2>&1 | grep "got:"
        # 3. Replace vendorHash with the hash from "got:"
        # Updated to include pgregory.net/rapid and github.com/goccy/go-json dependencies
        # If build fails, use fakeHash method documented above to recalculate
        vendorHash = null;
      in
      {
        packages = {
          bv = buildGoModule {
            pname = "bv";
            version = bvVersion;

            src = ./.;

            inherit vendorHash;

            subPackages = [ "cmd/bv" ];

            # CLI tests create isolated Git repositories and configure checkout
            # trust. Declare Git in the test environment instead of relying on
            # a host installation outside the Nix sandbox.
            nativeCheckInputs = [ pkgs.gitMinimal ];

            # buildGoModule sets GOTOOLCHAIN=local. Refuse a stale compiler
            # instead of silently ignoring the module's intended toolchain.
            preBuild = ''
              expected_go="$(awk '$1 == "go" { version = "go" $2 } $1 == "toolchain" { toolchain = $2 } END { print toolchain ? toolchain : version }' go.mod)"
              if [ "$(go env GOVERSION)" != "$expected_go" ]; then
                echo "Nix compiler does not match go.mod: expected $expected_go" >&2
                exit 1
              fi
            '';

            postInstall = ''
              if [ "$(go version "$out/bin/bv")" != "$out/bin/bv: go${go.version}" ]; then
                echo "Installed bv has an unexpected Go toolchain" >&2
                exit 1
              fi
              go version -m "$out/bin/bv"
              expected_net="$(awk '$1 == "golang.org/x/net" { print $2 }' go.mod)"
              built_net="$(go version -m "$out/bin/bv" | awk '$1 == "dep" && $2 == "golang.org/x/net" { print $3 }')"
              if [ -z "$expected_net" ] || [ "$built_net" != "$expected_net" ]; then
                echo "Installed bv does not contain the declared golang.org/x/net" >&2
                exit 1
              fi
            '';

            ldflags = [
              "-s"
              "-w"
              "-X github.com/Dicklesworthstone/beads_viewer/pkg/version.version=v${bvVersion}"
            ];

            meta = with pkgs.lib; {
              description = "Terminal UI for the Beads issue tracker with graph-aware triage";
              homepage = "https://github.com/Dicklesworthstone/beads_viewer";
              license = {
                fullName = "MIT License with OpenAI/Anthropic Rider";
                shortName = "MIT+OpenAI-Anthropic-Rider";
                url = "https://github.com/Dicklesworthstone/beads_viewer/blob/v${bvVersion}/LICENSE";
                free = false;
                redistributable = false;
              };
              maintainers = [ ];
              mainProgram = "bv";
              platforms = platforms.unix;
            };
          };

          default = self.packages.${system}.bv;
        };

        devShells.default = pkgs.mkShell {
          buildInputs = [
            go
            pkgs.gopls
            pkgs.gotools
            pkgs.go-tools
            pkgs.delve
          ];

          shellHook = ''
            echo "bv development environment"
            echo "Go version: $(go version)"
            echo ""
            echo "Available commands:"
            echo "  go build ./cmd/bv  - Build bv"
            echo "  go test ./...      - Run tests"
            echo "  nix build .#bv     - Build with Nix"
          '';
        };
      }
    );
}
