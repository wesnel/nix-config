{ config
, lib
, pkgs
, ... }:

let
  username = "wgn";
  realName = "Wesley Nelson";
  address = "${username}@${host.fastmail}";
  flavor = host.fastmail;

  host = {
    fastmail = "fastmail.com";
    wesnel = "wesnel.dev";
  };
in {
  config = {
    accounts.email.accounts = {
      fastmail = {
        inherit
          address
          flavor
          realName;

        primary = true;
        userName = address;
        passwordCommand = lib.mkIf config.programs.password-store.enable ''
          ${config.programs.password-store.package}/bin/pass show mail/imap/${host.fastmail}/${username}
        '';

        aliases = [
          "${username}@${host.wesnel}"
          "hire.me@${host.wesnel}"
        ];

        msmtp.enable = true;
        notmuch.enable = true;

        gpg = {
          key = "0x8AB4F50FF6C15D42";
          signByDefault = true;
        };

        mbsync = {
          enable = true;
          create = "maildir";
        };
      };
    };

    programs = {
      afew = {
        enable = true;

        extraConfig = ''
          [SpamFilter]
          [KillThreadsFilter]
          [ListMailsFilter]
          [ArchiveSentMailsFilter]
          [InboxFilter]
          [MailMover]
          folders = Inbox
          Inbox = 'tag:sent':Sent
        '';
      };

      mbsync = {
        enable = true;
      };

      msmtp = {
        enable = true;
      };

      notmuch = {
        enable = true;

        new = {
          tags = [ "new" ];
        };

        hooks = {
          preNew = ''
            ${config.programs.mbsync.package}/bin/mbsync --all
          '';

          postNew = ''
            ${pkgs.afew}/bin/afew -tn
          '';
        };
      };
    };
  };
}
