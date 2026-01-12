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

        version = "0.11.3";

        # To update vendorHash after go.mod/go.sum changes:
        # 1. Set vendorHash to: pkgs.lib.fakeHash
        # 2. Run: nix build .#bv 2>&1 | grep "got:"
        # 3. Replace vendorHash with the hash from "got:"
        vendorHash = "sha256-V8Bl5lW9vd7o1ZcQ6rvs3WJ1ueYX7xKnHTyRAASHlng=";
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
              "-X github.com/Dicklesworthstone/beads_viewer/pkg/version.Version=v${version}"
            ];

            meta = with pkgs.lib; {
              description = "Terminal UI for the Beads issue tracker with graph-aware triage";
              homepage = "https://github.com/Dicklesworthstone/beads_viewer";
              license = licenses.mit;
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
