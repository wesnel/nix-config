{
  username,
  pkgs,
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.fish;
in {
  options.wgn.darwin.fish = {
    enable = mkEnableOption "Enables my Fish setup for Darwin";
  };

  config = mkIf cfg.enable {
    programs.fish = {
      enable = true;

      interactiveShellInit =
        # TODO: Maybe there is a better place to put work-specific config like this.
        # TODO: Use my ep package for fetching password instead of invoking emacs directly.
        # TODO: Modify ep package to be able to batch-export passwords as environment variables so that we don't need this crazy script.
        # TODO: This depends upon certain secrets being available in `~/.authinfo.gpg`, so it is not exactly deterministic.
        mkIf config.wgn.darwin.emacs.enable ''
          set -gx ARTIFACTORY_PYPI_USERNAME ${username}

          gpg --card-status > /dev/null

          # Fetch all passwords in one Emacs call, each line corresponds to a password
          set secrets (emacs --quick --batch --load 'auth-source' --eval "
            (princ (mapconcat
              (lambda (entry)
                (condition-case nil
                    (auth-info-password
                     (nth 0 (auth-source-search
                             :host (car entry)
                             :user (cdr entry)
                             :max 1)))
                  (file-error \"\")))
              '((\"artifactory.gcp.shipttech.com\" . \"$ARTIFACTORY_PYPI_USERNAME\")
                (\"github.com\" . \"wesnel\")
                (\"huggingface.co\" . \"${username}@shipt.com\")
                (\"datadoghq.com\" . \"${username}@shipt.com\")
                (\"datadoghq.com\" . \"shipt\")
                (\"shipt.okta.com\" . \"${username}@shipt.com\"))
              \" \"))")

          # Split output into individual lines
          set secrets (echo $secrets | string split ' ')

          # Assign secrets to respective environment variables
          set -l index 1
          for var in ARTIFACTORY_PYPI_PASSWORD GITHUB_TOKEN HF_TOKEN DD_APP_KEY DD_API_KEY SHIPT_PASSWORD
              set -gx $var $secrets[$index]
              set index (math $index + 1)
          end

          set -gx SHIPT_USERNAME $ARTIFACTORY_PYPI_USERNAME
          set -gx POETRY_HTTP_BASIC_SHIPT_RESOLVE_USERNAME $ARTIFACTORY_PYPI_USERNAME
          set -gx POETRY_HTTP_BASIC_SHIPT_RESOLVE_PASSWORD $ARTIFACTORY_PYPI_PASSWORD
        '';
    };

    environment = with pkgs; {
      shells = [fish];
    };

    users.users.${username} = {
      shell = config.programs.fish.package;
    };
  };
}
