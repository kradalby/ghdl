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
    { self
    , nixpkgs
    , flake-utils
    , flake-checks
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
        pkgs = nixpkgs.legacyPackages.${system};
        fc = flake-checks.lib;
        common = {
          inherit pkgs;
          root = ./.;
          pname = "ghdl";
          version = "0.1.0";
          vendorHash = hashes.vendor.sri;
          goPkg = pkgs.go_1_26;
          subPackages = [ "cmd/ghdl" ];
        };
        # The dashboard generator is its own build so the Grafana Foundation SDK
        # stays out of the service binary. Its built output is captured as a
        # derivation below — running it *is* the validation (bad JSON exits non-zero).
        dashboard = fc.goBuild (common // {
          pname = "ghdl-dashboard";
          subPackages = [ "cmd/dashboard" ];
        });
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
            pkgs.go_1_26
            pkgs.gopls
            pkgs.golangci-lint
            pkgs.gofumpt
            (fc.formatter common)
            pkgs.prek
            pkgs.sqlc
            pkgs.govulncheck
            pkgs.gnumake
          ];
        };
        checks = {
          build = fc.goBuild common;
          gotest = fc.goTest (common // { goRace = true; });
          golangci-lint = fc.goLint common;
          formatting = fc.goFormat common;
        }
        # NixOS module evaluation needs a Linux system.
        // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux {
          module-eval = import ./module-eval.nix { inherit pkgs self nixpkgs system; };
        };
      }
    );
}
