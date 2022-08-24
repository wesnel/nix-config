inputs@
{ pkgs
, config
, ...
}:

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
  imports = [
    ../../../programs/msmtp
    ../../../programs/notmuch
  ];

  accounts.email.accounts = {
    fastmail = {
      inherit address
              flavor
              realName;

      primary = true;
      userName = address;
      passwordCommand = "pass show mail/imap/${host.fastmail}/${username}";

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
}
