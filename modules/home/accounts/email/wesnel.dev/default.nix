inputs@
{ pkgs
, config
, ...
}:

let
  host = {
    custom = "wesnel.dev";
    fastmail = "fastmail.com";
  };

  realName = "Wesley Nelson";
  username = "wgn";
  address = "${username}@${host.custom}";
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
    passwordCommand = "pass show mail/imap/${host.custom}/${username}";

    imap.host = "imap.${host.fastmail}";
    smtp.host = "smtp.${host.fastmail}";

    msmtp.enable = true;
    notmuch.enable = true;

    signature = {
      text = ''
        ${realName}
        https://${host.custom}
      '';

      showSignature = "append";
    };

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
