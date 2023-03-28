inputs@
  { trivialBuild
  , fetchFromGitHub
  , markdown-mode
  , ...
  }:

let
  rev = "60e3b05220acff858a5b6fc43b8fa49dd886548a";
in trivialBuild rec {
  pname = "chatgpt-shell";
  version = rev;

  src = fetchFromGitHub {
    owner = "xenodium";
    repo = "chatgpt-shell";
    sha256 = "sha256-hXt2KClUvZa8M6AobUrpSBUtf4uk4WiLO/tHtc6eSuE=";
    inherit rev;
  };

  propagatedUserEnvPkgs = [
    markdown-mode
  ];

  buildInputs = propagatedUserEnvPkgs;
}
