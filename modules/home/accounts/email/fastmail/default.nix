inputs@
{ pkgs
, config
, ...
}:

let
  username = "wgn";
  host = "fastmail.com";
  realName = "Wesley Nelson";
  address = "${username}@${host}";
in {
  imports = [
    ../../../programs/afew
    ../../../programs/msmtp
    ../../../programs/notmuch
  ];

  accounts.email.accounts.wesnel-dev = {
    inherit address
            realName;

    primary = true;

    userName = address;
    passwordCommand = "pass show mail/imap/${host}/${username}";

    imap.host = "imap.${host}";
    smtp.host = "smtp.${host}";

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
}
