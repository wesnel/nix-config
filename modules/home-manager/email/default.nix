{
  config,
  lib,
  signingKey,
  ...
}:
with lib; let
  cfg = config.wgn.home.email;
in {
  options.wgn.home.email = {
    enable = mkEnableOption "Enables my Email setup for home-manager";
  };

  config = let
    username = "wgn";
    realName = "Wesley Nelson";
    address = "${username}@${host.fastmail}";
    flavor = host.fastmail;

    host = {
      fastmail = "fastmail.com";
      wesnel = "wesnel.dev";
      wgn = "wgn.dev";
    };
  in
    mkIf cfg.enable {
      accounts.email.accounts = {
        fastmail = {
          inherit
            address
            flavor
            realName
            ;

          primary = true;
          userName = address;

          aliases = [
            "${username}@${host.wgn}"
            "${username}@${host.wesnel}"
            "hire.me@${host.wesnel}"
          ];

          notmuch = {
            enable = true;
          };

          gpg = {
            key = signingKey;
            signByDefault = true;
          };

          mujmap = {
            enable = true;

            settings = {
              password_command = lib.mkIf config.programs.password-store.enable ''
                ${config.programs.fish.package}/bin/fish -c '${config.programs.password-store.package}/bin/pass show mail/imap/${host.fastmail}/${username}'
              '';
            };
          };
        };
      };

      programs = {
        mujmap = {
          enable = true;
        };

        notmuch = {
          enable = true;

          hooks = {
            preNew = lib.mkIf config.programs.mujmap.enable ''
              ${config.programs.fish.package}/bin/fish -c '${config.programs.mujmap.package}/bin/mujmap -C ${config.accounts.email.accounts.fastmail.maildir.absPath} sync'
            '';
          };
        };
      };
    };
}
