inputs@
{ pkgs
, ...
}:

{
  programs = {
    zsh = {
      enable = true;

      sessionVariables = {
        EDITOR = "nvim";
      };

      shellAliases = {
        ls = "${pkgs.exa}/bin/exa -la --git --color=always";
        tree = "${pkgs.exa}/bin/exa -la --tree --git --color=always";
      };

      oh-my-zsh = {
        enable = true;
        theme = "robbyrussell";

        plugins = [
          "git"
          "sudo"
        ];
      };
    };
  };
}
