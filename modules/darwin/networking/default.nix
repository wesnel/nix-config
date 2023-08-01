{ computerName
, ... }:

{
  imports = [
    ../../../components/networking
  ];

  networking = {
    inherit computerName;
  };
}
