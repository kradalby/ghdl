# Eval-time smoke test for the NixOS module: assemble a minimal system and
# assert the rendered ExecStart, so a broken option or service definition fails
# `nix flake check` without spinning up a VM.
{ pkgs, self, nixpkgs, system }:
let
  inherit (pkgs) lib;

  execStartFor =
    cfg:
    (import (nixpkgs + "/nixos/lib/eval-config.nix") {
      inherit system;
      modules = [
        self.nixosModules.default
        {
          boot.loader.grub.enable = false;
          fileSystems."/" = {
            device = "/dev/sda1";
            fsType = "ext4";
          };
          system.stateVersion = "24.11";
          services.ghdl = cfg;
        }
      ];
    }).config.systemd.services.ghdl.serviceConfig.ExecStart;

  default = execStartFor {
    enable = true;
    environmentFile = "/run/secrets/ghdl";
  };

  custom = execStartFor {
    enable = true;
    hostname = "dlmetrics";
    githubRepos = [ "a/b" "c/d" ];
  };

  check = cond: msg: if cond then true else throw "module-eval: ${msg}";
in
assert check (lib.hasInfix "--hostname=ghdl" default) "default ExecStart missing --hostname: ${default}";
assert check (lib.hasInfix "--state-dir=/var/lib/ghdl" default) "default ExecStart missing --state-dir: ${default}";
assert check (lib.hasInfix "--hostname=dlmetrics" custom) "custom ExecStart missing overridden hostname: ${custom}";
assert check (lib.hasInfix "--github-repos=a/b,c/d" custom) "custom ExecStart missing joined repos: ${custom}";
pkgs.runCommand "ghdl-module-eval-ok" { } "touch $out"
