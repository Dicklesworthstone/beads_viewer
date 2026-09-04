{
  description = "bv - Terminal UI for the Beads issue tracker";

  inputs = {
    # Use nixpkgs unstable for Go 1.25+ support
    # go.mod requires go 1.25, which isn't in stable nixpkgs yet
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };

        version = "0.23.0";

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
          bv = pkgs.buildGoModule {
            pname = "bv";
            inherit version;

            src = ./.;

            inherit vendorHash;

            subPackages = [ "cmd/bv" ];

            ldflags = [
              "-s"
              "-w"
              "-X github.com/Dicklesworthstone/beads_viewer/pkg/version.version=v${version}"
            ];

            meta = with pkgs.lib; {
              description = "Terminal UI for the Beads issue tracker with graph-aware triage";
              homepage = "https://github.com/Dicklesworthstone/beads_viewer";
              license = {
                fullName = "MIT License with OpenAI/Anthropic Rider";
                shortName = "MIT+OpenAI-Anthropic-Rider";
                url = "https://github.com/Dicklesworthstone/beads_viewer/blob/v${version}/LICENSE";
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
          buildInputs = with pkgs; [
            go
            gopls
            gotools
            go-tools
            delve
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
