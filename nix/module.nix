{ self }:
{
  config,
  pkgs,
  lib,
  ...
}:
let
  cfg = config;
  cominConfigLib = import ./comin-config.nix { inherit config pkgs lib; };
  inherit (cominConfigLib) cominConfigYaml;

  inherit (pkgs.stdenv.hostPlatform) system;
  inherit (cfg.services.comin) package;

  remoteWithAuth = lib.findFirst (r: r.auth.access_token_path != "") null cfg.services.comin.remotes;

  # This is needed because Nix's flake fetcher shells out to git for
  # submodule operations, and git has no other way to authenticate.
  gitAskpass = pkgs.writeShellScript "comin-git-askpass" ''
    case "$1" in
      Username*) echo "${remoteWithAuth.auth.username}" ;;
      Password*) cat "${remoteWithAuth.auth.access_token_path}" ;;
    esac
  '';
in
{
  imports = [ ./module-options.nix ];
  config = lib.mkIf cfg.services.comin.enable {
    assertions = [
      {
        assertion = cfg.services.comin.niks3 == null || cfg.services.comin.remotes == [ ];
        message = "comin: choose either niks3 or Git remotes.";
      }
      {
        assertion = package != null;
        message = "`services.comin.package` cannot be null.";
      }
      # If the package is null and our `system` isn't supported by the Flake, it's probably safe to show this error message
      {
        assertion = package == null -> lib.elem system (lib.attrNames self.packages);
        message = "comin: ${system} is not supported by the Flake.";
      }
    ]
    ++ lib.forEach cfg.services.comin.remotes (remote: {
      assertion = !(remote.auth.access_token_path != "" && remote.auth.ssh_deploy_key_path != "");
      message = "comin: remote `${remote.name}` sets both `auth.access_token_path` and `auth.ssh_deploy_key_path`; set at most one.";
    });

    systemd.user.services.comin-desktop = lib.mkIf cfg.services.comin.desktop.enable {
      wantedBy = [ "graphical-session.target" ];
      path = [ pkgs.libnotify ];
      after = [ "graphical-session.target" ];
      partOf = [ "graphical-session.target" ];
      serviceConfig = {
        ExecStart = lib.escapeShellArgs (
          [
            (lib.getExe package)
            "desktop"
            "--title"
            cfg.services.comin.desktop.title
          ]
          ++ lib.optional cfg.services.comin.desktop.interactive "--interactive"
        );
        Restart = "on-failure";
        RestartSec = 3;
      };
    };

    environment.systemPackages = [
      package
    ]
    ++ lib.optional (cfg.services.comin.desktop.enable && cfg.services.comin.desktop.interactive) (
      pkgs.makeDesktopItem {
        name = "comin-updates";
        desktopName = "System updates";
        genericName = "Show the downloaded update";
        exec = "${pkgs.systemd}/bin/systemctl --user restart comin-desktop.service";
        icon = "system-software-update";
        categories = [ "System" ];
        extraConfig."Name[ru]" = "Обновление системы";
      }
    );
    networking.firewall.allowedTCPPorts = lib.optional cfg.services.comin.exporter.openFirewall cfg.services.comin.exporter.port;
    # Use package from overlay first, then Flake package if available
    services.comin.package = lib.mkDefault pkgs.comin or self.packages.${system}.comin or null;
    systemd.services.comin = {
      wantedBy = [ "multi-user.target" ];
      path = [
        config.nix.package
        config.programs.ssh.package
      ];
      # The comin service is restarted by comin itself when it
      # detects the unit file changed.
      restartIfChanged = false;
      environment = {
        CURL_CA_BUNDLE = cfg.security.pki.caBundle;
      }
      // (lib.optionalAttrs
        (cfg.services.comin.niks3 != null && cfg.services.comin.niks3.aws_credentials_file != "")
        {
          # Root's nix-store may open the local store directly, bypassing the
          # daemon. Both paths must see the same read-only AWS profile file.
          AWS_SHARED_CREDENTIALS_FILE = cfg.services.comin.niks3.aws_credentials_file;
        }
      )
      // cfg.networking.proxy.envVars
      // (lib.optionalAttrs (cfg.services.comin.submodules && remoteWithAuth != null) {
        GIT_ASKPASS = gitAskpass;
      });
      serviceConfig = {
        ExecStart =
          (lib.getExe package)
          + (lib.optionalString cfg.services.comin.debug " --debug ")
          + " run "
          + "--config ${cominConfigYaml}";
        Restart = "always";
      };
    };
  };
}
