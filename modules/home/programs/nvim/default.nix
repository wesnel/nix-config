inputs@
{ pkgs
, ...
}:

{
  programs.neovim = {
    enable = true;

    plugins = with pkgs.vimPlugins; [
      cmp_luasnip
      cmp-nvim-lsp
      fugitive
      gitgutter
      lualine-nvim
      luasnip
      nerdtree
      nvim-base16
      nvim-cmp
      nvim-lspconfig
      nvim-treesitter
      sensible
      sleuth
    ];

    extraConfig = ''
      lua << EOF
        ${builtins.readFile ./config.lua}
      EOF
    '';
  };
}
