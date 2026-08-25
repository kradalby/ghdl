{
  description = "ghdl — scrape GitHub/Docker Hub/GHCR download counts into SQLite, serve for Grafana";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    flake-checks.url = "github:kradalby/flake-checks";
    flake-checks.inputs.nixpkgs.follows = "nixpkgs";
    flake-checks.inputs.flake-utils.follows = "flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      flake-checks,
    }:
    let
      hashes = builtins.fromJSON (builtins.readFile ./flakehashes.json);
    in
    {
      overlays.default = _final: prev: {
        ghdl = self.packages.${prev.system}.default;
      };
      nixosModules.default = import ./module.nix self;
    }
    // flake-utils.lib.eachDefaultSystem (
      system:
      let
        # nixpkgs' bare `go` is still 1.26 while this repo targets 1.27, so the
        # Go version is named explicitly everywhere as `go_latest`. The Go dev
        # tools that ship *wrapped with a `go` on PATH* (goimports, via gotools)
        # must be rebuilt against it too: otherwise that wrapper's older `go`
        # sees the 1.27 directive in go.mod and GOTOOLCHAIN=auto tries to fetch
        # a toolchain from inside the network-less treefmt sandbox.
        goOverlay = _final: prev: {
          gofumpt = prev.gofumpt.override { buildGoModule = prev.buildGoLatestModule; };
          gotools = prev.gotools.override {
            buildGoModule = prev.buildGoLatestModule;
            go = prev.go_latest;
          };
        };
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ goOverlay ];
        };
        fc = flake-checks.lib;
        common = {
          inherit pkgs;
          root = ./.;
          pname = "ghdl";
          version = "0.1.0";
          vendorHash = hashes.vendor.sri;
          goPkg = pkgs.go_latest;
          subPackages = [ "cmd/ghdl" ];
          # db/db.go embeds schema.sql via //go:embed; flake-checks' src filter
          # whitelists .go files, so the embedded schema must be added explicitly.
          extraSrc = [ ./db/schema.sql ];
        };
        # The dashboard generator is its own build so the Grafana Foundation SDK
        # stays out of the service binary. Its built output is captured as a
        # derivation below — running it *is* the validation (bad JSON exits non-zero).
        dashboard = fc.goBuild (
          common
          // {
            pname = "ghdl-dashboard";
            subPackages = [ "cmd/dashboard" ];
          }
        );
      in
      {
        packages = {
          default = (fc.goBuild common).overrideAttrs (_: {
            meta.mainProgram = "ghdl";
          });
          inherit dashboard;
          grafanaDashboards = pkgs.runCommand "ghdl-grafana-dashboards" { } ''
            mkdir -p $out
            ${dashboard}/bin/dashboard -out $out
          '';
        };
        formatter = fc.formatter common;
        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go_latest
            pkgs.gopls
            pkgs.golangci-lint
            pkgs.gofumpt
            (fc.formatter common)
            pkgs.prek
            pkgs.sqlc
            pkgs.govulncheck
            pkgs.gnumake
          ];
          shellHook = ''
            # Never fetch a toolchain: a go.mod ahead of nixpkgs' Go must be a
            # loud error, not a silent download from go.dev outside the store.
            export GOTOOLCHAIN=local
          '';
        };
        checks = {
          build = fc.goBuild common;
          gotest = fc.goTest (common // { goRace = true; });
          golangci-lint = fc.goLint common;
          formatting = fc.goFormat common;
          # db/dbsqlc is sqlc output, not `go generate` output: there are no
          # //go:generate directives, so the drift check drives sqlc directly.
          # sqlc reads sqlc.yaml + db/schema.sql + db/queries/, all of which the
          # default .go-only src filter drops, hence the extraSrc additions.
          generate = fc.goGenerate (
            common
            // {
              extraSrc = common.extraSrc ++ [
                ./sqlc.yaml
                ./db/queries
              ];
              nativeCheckInputs = [ pkgs.sqlc ];
              generateCommand = "sqlc generate";
            }
          );
        }
        # NixOS module evaluation needs a Linux system.
        // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux {
          module-eval = import ./module-eval.nix {
            inherit
              pkgs
              self
              nixpkgs
              system
              ;
          };
        };
      }
    );
}
