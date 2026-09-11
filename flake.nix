{
  description = "cc-mimicry — CLIProxyAPI CGO c-shared plugin";

  inputs = {
    # nixos-25.11 lacks go_1_26; use unstable until nixos-26.05 is available.
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs =
    inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
        "x86_64-darwin"
      ];

      perSystem =
        { system, ... }:
        let
          pkgs = import inputs.nixpkgs { inherit system; };
        in
        {
          checks.build-check = pkgs.runCommand "build-check" { } ''
            touch $out
          '';

          # Capture toolchain for scripts/recon/capture-live.sh, exposed so the
          # script resolves these through this flake's lock rather than whatever
          # the caller's nixpkgs registry happens to point at.
          #
          # Two outputs, not one env: the mitmproxy app and a python carrying the
          # mitmproxy library both ship bin/mitmdump and collide in a buildEnv.
          packages.recon-proxy = pkgs.mitmproxy;
          packages.recon-python = pkgs.python3.withPackages (ps: [ ps.mitmproxy ]);

          devShells.default = pkgs.mkShell {
            packages = [
              pkgs.go_1_26
              pkgs.gcc
              pkgs.golangci-lint
              pkgs.gofumpt
              pkgs.govulncheck
              pkgs.gotestsum
              pkgs.lefthook
              pkgs.typos
              pkgs.act
              pkgs.actionlint
            ];
            env = {
              GOTOOLCHAIN = "local";
              CGO_ENABLED = "1";
            };
            shellHook = ''
              ${pkgs.lefthook}/bin/lefthook install >/dev/null 2>&1 || true
              echo "cc-mimicry devshell — Go $(go version 2>/dev/null | awk '{print $3}'), CGO enabled"
              command -v dg >/dev/null || echo "dg not on PATH — decision-graph checks unavailable"
              echo ""
              echo "  Commands:"
              echo "    ./build.sh               build cc-mimicry.so -> dist/"
              echo "    make build               same via Makefile"
              echo "    go test -race ./...       run tests"
              echo "    golangci-lint run ./...   lint"
              echo "    gofumpt -l .              format check"
              echo "    govulncheck ./...         vuln scan"
              echo ""
            '';
          };
        };
    };
}
