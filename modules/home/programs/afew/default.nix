inputs@
{ config
, lib
, pkgs
, ...
}:

{
  programs.afew = {
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
}
