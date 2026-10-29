{
  config,
  lib,
  pkgs,
  key,
  ...
}:
with lib; let
  cfg = config.wgn.home.git;
in {
  options.wgn.home.git = {
    enable = mkEnableOption "Enables my Git setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      gh
    ];

    sops = {
      secrets = {
        email = {};
        name = {};
      };

      templates = {
        "git.inc" = {
          content = ''
            [user]
              email = "${config.sops.placeholder.email}"
              name = "${config.sops.placeholder.name}"
          '';
        };
      };
    };

    programs.delta = {
      enable = true;
      enableGitIntegration = true;
    };

    programs.git = {
      enable = true;
      lfs.enable = true;

      includes =
        [
          {
            path = config.sops.templates."git.inc".path;
          }
          # TODO: Add work-specific git config using includes.*.condition
        ]
        # These repositories are mirrored on sourcehut, which is the canonical
        # public copy of emacs-config, so a push to origin goes to both. A
        # push URL replaces the fetch URL for pushing, which is why GitHub is
        # listed as well. Fetch rewrites the clones' scp-style GitHub URL to
        # anonymous HTTPS; the distinct ssh:// push URL keeps key-backed SSH.
        ++ map (repo: {
          condition = "gitdir:~/git/github.com/wesnel/${repo}/";
          contents = {
            url."https://github.com/wesnel/${repo}.git".insteadOf = "git@github.com:wesnel/${repo}.git";
            remote.origin.pushurl = [
              "ssh://git@github.com/wesnel/${repo}.git"
              "git@git.sr.ht:~wgn/${repo}"
            ];
          };
        }) ["nix-config" "emacs-config"];

      settings = {
        commit = {
          gpgsign = true;
        };

        # Nix's git names the macOS keychain as a credential helper in its
        # system config, and the keychain holds a GitHub login, with which
        # `git push https://github.com/...` would publish without a key touch.
        # An empty helper empties the list for github.com: HTTPS fetches stay
        # anonymous and authenticated pushes use SSH. gh and pr-loop keep
        # their own login.
        credential."https://github.com".helper = "";

        # Runs before every push from every repository, beside the
        # repository's own pre-push hook, and refuses to publish the
        # operator's unsigned commits, or a private GitHub repository's
        # history in a public one. Only a git that
        # supports hooks in config (2.54 and later) runs it, so a distro's
        # older git ahead of this one on PATH skips it without a word.
        hook.push-guard = {
          event = "pre-push";
          command = getExe' config.wgn.home.llm.agentTools "git-push-guard";
        };

        init = {
          defaultBranch = "main";
        };

        tag = {
          gpgsign = true;
        };

        user = {
          signingkey = key;
        };
      };
    };
  };
}
