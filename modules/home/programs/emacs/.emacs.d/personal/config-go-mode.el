(add-hook 'go-mode-hook
          (lambda ()
            ;; Set tab width
            (setq tab-width 2)
            ;; Disable highlighting of every tab character
            (whitespace-toggle-options 'tabs)
            ;; Format before saving
            (add-hook 'before-save-hook 'gofmt-before-save)))
