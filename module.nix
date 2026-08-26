# NixOS module for ghdl. Import via the flake's nixosModules.default.
#
#   services.ghdl = {
#     enable = true;
#     environmentFile = config.age.secrets.ghdl.path; # GHDL_GITHUB_TOKEN=... TS_AUTHKEY=...
#   };
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.ghdl;
  # A Go time.Duration string (e.g. "24h", "1h30m"), validated at eval time so a
  # typo fails the build instead of crash-looping the service.
  duration = lib.types.strMatching "0|([0-9]+(ns|us|ms|s|m|h))+";
in
{
  options.services.ghdl = {
    enable = lib.mkEnableOption "ghdl, a GitHub/Docker Hub/GHCR download-metrics scraper";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "ghdl flake package";
      description = "The ghdl package to run.";
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "ghdl";
      description = "User to run as. A static user (not DynamicUser) so litestream can join its group to back up the database.";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "ghdl";
      description = "Group owning the state directory and database.";
    };

    hostname = lib.mkOption {
      type = lib.types.str;
      default = "ghdl";
      description = "Tailnet hostname to serve as (its own tsnet node).";
    };

    localAddr = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:9091";
      description = "Loopback address for the local HTTP listener.";
    };

    interval = lib.mkOption {
      type = duration;
      default = "24h";
      description = "How often to scrape the sources.";
    };

    githubRepos = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "juanfont/headscale" ];
      description = "owner/repo list to track GitHub release downloads for.";
    };

    dockerhubRepos = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "headscale/headscale" ];
      description = "namespace/repo list to track Docker Hub pulls for.";
    };

    ghcrRepos = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "juanfont/headscale" ];
      description = "owner/package list to track GHCR pulls for.";
    };

    environmentFile = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        Path to an EnvironmentFile sourced by the service. Carries the secrets:
        GHDL_GITHUB_TOKEN=... (GitHub API, raises the rate limit) and
        TS_AUTHKEY=... (unattended tailnet enrolment). Keep it out of the Nix
        store (e.g. an agenix/ragenix secret).
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    users.users.${cfg.user} = {
      isSystemUser = true;
      group = cfg.group;
    };
    users.groups.${cfg.group} = { };

    systemd.services.ghdl = {
      description = "ghdl download-metrics scraper";
      wantedBy = [ "multi-user.target" ];
      after = [
        "network-online.target"
        "nss-lookup.target"
      ];
      wants = [
        "network-online.target"
        "nss-lookup.target"
      ];

      serviceConfig = {
        ExecStart = lib.escapeShellArgs [
          (lib.getExe cfg.package)
          "--hostname=${cfg.hostname}"
          "--state-dir=/var/lib/ghdl"
          "--local-addr=${cfg.localAddr}"
          "--interval=${cfg.interval}"
          "--github-repos=${lib.concatStringsSep "," cfg.githubRepos}"
          "--dockerhub-repos=${lib.concatStringsSep "," cfg.dockerhubRepos}"
          "--ghcr-repos=${lib.concatStringsSep "," cfg.ghcrRepos}"
        ];

        User = cfg.user;
        Group = cfg.group;
        StateDirectory = "ghdl";
        # setgid so litestream-created -wal/-shm inherit the ghdl group (litestream
        # joins it to replicate the db); group-writable via UMask below.
        StateDirectoryMode = "2770";
        UMask = "0007";
        Restart = "always";
        RestartSec = "30s";

        # Graceful shutdown: SIGTERM to the main pid so it flushes and drains;
        # SIGKILL only mops up stragglers at the stop timeout.
        KillMode = "mixed";
        TimeoutStopSec = "30s";

        # tsnet is a Tailscale node: it needs CAP_NET_ADMIN to set the bypass
        # socket mark (SO_MARK), or it breaks on multi-homed hosts.
        AmbientCapabilities = [ "CAP_NET_ADMIN" ];
        CapabilityBoundingSet = [ "CAP_NET_ADMIN" ];

        # Hardening: ghdl needs outbound network (GitHub/Docker Hub/GHCR +
        # Tailscale) and its own state directory; nothing else.
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        # AF_UNIX is required for tsnet's local API socket.
        RestrictAddressFamilies = [
          "AF_UNIX"
          "AF_INET"
          "AF_INET6"
          "AF_NETLINK"
        ];
        RestrictNamespaces = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        SystemCallFilter = [ "@system-service" ];
        SystemCallErrorNumber = "EPERM";
      }
      // lib.optionalAttrs (cfg.environmentFile != null) {
        EnvironmentFile = cfg.environmentFile;
      };
    };
  };
}
