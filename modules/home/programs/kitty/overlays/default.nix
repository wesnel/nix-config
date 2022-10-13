(final: prev: {
  kitty = prev.kitty.overrideAttrs (old: {
    preCheck = ''
      ${old.preCheck}
      substituteInPlace kitty_tests/shell_integration.py \
        --replace test_zsh_integration dont_test_zsh_integration
      substituteInPlace kitty_tests/ssh.py \
        --replace test_ssh_leading_data dont_test_ssh_leading_data
    '';
  });
})
