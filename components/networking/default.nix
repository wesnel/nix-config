{ computerName
, ... }:

{
  networking = {
    hostName = computerName;
  };
}
