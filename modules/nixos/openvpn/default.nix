{ homeDirectory
, ... }:

{
  services.openvpn.servers = {
    shipt = {
      config = "config ${homeDirectory}/profile-2994.ovpn";
    };
  };
}
