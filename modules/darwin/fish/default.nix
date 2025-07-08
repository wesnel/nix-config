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
        mkIf config.wgn.darwin.emacs.enable ''
          set -gx ARTIFACTORY_PYPI_USERNAME ${username}
          set -gx ARTIFACTORY_PYPI_PASSWORD (gpg --card-status > /dev/null && emacs --quick --batch --load 'auth-source' --eval "(condition-case nil (princ (auth-info-password (nth 0 (auth-source-search :host \"artifactory.gcp.shipttech.com\" :user \"$ARTIFACTORY_PYPI_USERNAME\" :max 1)))) (file-error nil))")

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
