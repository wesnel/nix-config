{ computerName
, ... }:

{
  networking = {
    hostName = computerName;
  };
}