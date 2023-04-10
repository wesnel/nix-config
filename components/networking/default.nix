{ computerName
, ... }:

{
  networking = {
    inherit computerName;
    hostName = computerName;
  };
}
