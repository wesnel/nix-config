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
        # TODO: Modify ep package to be able to batch-export passwords as environment variables so that we don't load emacs N times on shell startup!
        # TODO: This depends upon certain secrets being available in `~/.authinfo.gpg`, so it is not exactly deterministic.
        mkIf config.wgn.darwin.emacs.enable ''
          set -gx ARTIFACTORY_PYPI_USERNAME ${username}
          set -gx ARTIFACTORY_PYPI_PASSWORD (gpg --card-status > /dev/null && emacs --quick --batch --load 'auth-source' --eval "(condition-case nil (princ (auth-info-password (nth 0 (auth-source-search :host \"artifactory.gcp.shipttech.com\" :user \"$ARTIFACTORY_PYPI_USERNAME\" :max 1)))) (file-error nil))")

          set -gx POETRY_HTTP_BASIC_SHIPT_RESOLVE_USERNAME $ARTIFACTORY_PYPI_USERNAME
          set -gx POETRY_HTTP_BASIC_SHIPT_RESOLVE_PASSWORD $ARTIFACTORY_PYPI_PASSWORD

          set -gx GITHUB_TOKEN (gpg --card-status > /dev/null && emacs --quick --batch --load 'auth-source' --eval "(condition-case nil (princ (auth-info-password (nth 0 (auth-source-search :host \"github.com\" :user \"wesnel\" :max 1)))) (file-error nil))")
          set -gx HF_TOKEN (gpg --card-status > /dev/null && emacs --quick --batch --load 'auth-source' --eval "(condition-case nil (princ (auth-info-password (nth 0 (auth-source-search :host \"huggingface.co\" :user \"${username}@shipt.com\" :max 1)))) (file-error nil))")
          set -gx DD_APP_KEY (gpg --card-status > /dev/null && emacs --quick --batch --load 'auth-source' --eval "(condition-case nil (princ (auth-info-password (nth 0 (auth-source-search :host \"datadoghq.com\" :user \"${username}@shipt.com\" :max 1)))) (file-error nil))")
          set -gx DD_API_KEY (gpg --card-status > /dev/null && emacs --quick --batch --load 'auth-source' --eval "(condition-case nil (princ (auth-info-password (nth 0 (auth-source-search :host \"datadoghq.com\" :user \"shipt\" :max 1)))) (file-error nil))")
          set -gx SHIPT_USERNAME '${username}'
          set -gx SHIPT_PASSWORD (gpg --card-status > /dev/null && emacs --quick --batch --load 'auth-source' --eval "(condition-case nil (princ (auth-info-password (nth 0 (auth-source-search :host \"shipt.okta.com\" :user \"${username}@shipt.com\" :max 1)))) (file-error nil))")
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
