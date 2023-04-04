;;; init.el --- Wesley's Emacs Config  -*- lexical-binding: t; -*-

;; Copyright (C) 2023 Wesley Nelson

;; Author: Wesley Nelson <wgn@wesnel.dev>
;; Maintainer: Wesley Nelson <wgn@wesnel.dev>

;;; Commentary:

;;; Code:
(let ((gc-cons-threshold most-positive-fixnum))
  ;;
  ;; Setup
  ;;

  (eval-when-compile
    (require 'use-package))

  ;; Store all backup and autosave files in the tmp dir.
  (setq backup-directory-alist
        `((".*" . ,temporary-file-directory)))
  (setq auto-save-file-name-transforms
        `((".*" ,temporary-file-directory t)))

  ;; Revert buffers automatically when underlying files are changed externally.
  (global-auto-revert-mode t)

  ;; Required for :bind in use-package.
  (use-package bind-key
    :ensure t)

  ;; Inherit environment variables in Emacs.
  (when (memq window-system '(mac ns x))
    (use-package exec-path-from-shell
      :ensure t

      :config
      (setq exec-path-from-shell-variables
            '("SSH_AUTH_SOCK"
              "GPG_TTY"
              "PATH"))
      (exec-path-from-shell-initialize)))

  ;;
  ;; Visuals
  ;;

  ;; Mode line settings.
  (line-number-mode t)
  (column-number-mode t)
  (size-indication-mode t)

  ;; Show line numbers at the beginning of each line.
  (global-display-line-numbers-mode +1)

  ;; Configure whitespace preferences.
  (use-package whitespace
    :config
    (setq whitespace-style '(face tabs empty trailing))

    (defun enable-whitespace ()
      (add-hook 'before-save-hook 'whitespace-cleanup nil t)
      (whitespace-mode +1))
    (add-hook 'text-mode-hook 'enable-whitespace)
    (add-hook 'prog-mode-hook 'enable-whitespace))

  ;; Color scheme.
  (load-theme 'modus-vivendi t)

  ;; Syntax highlighting.
  (setq treesit-font-lock-level 4)

  ;; Highlight TODO comments.
  (use-package hl-todo
    :ensure t

    :config
    (global-hl-todo-mode +1))

  ;;
  ;; Interface
  ;;

  ;; Enable y/n answers.
  (fset 'yes-or-no-p 'y-or-n-p)

  ;; Displays available keybindings in a pop-up.
  (use-package which-key
    :ensure t

    :config
    (which-key-mode +1))

  (use-package vertico
    :ensure t

    :init
    ;; Do not allow the cursor in the minibuffer prompt.
    (setq minibuffer-prompt-properties '(read-only t cursor-intangible t face minibuffer-prompt))
    (add-hook 'minibuffer-setup-hook #'cursor-intangible-mode)

    ;; Emacs 28: Hide commands in M-x which do not work in the current mode.
    ;; Vertico commands are hidden in normal buffers.
    (setq read-extended-command-predicate #'command-completion-default-include-p)

    ;; Enable recursive minibuffers.
    (setq enable-recursive-minibuffers t)

    :config
    (vertico-mode +1)

    ;; Grow and shrink the Vertico minibuffer.
    (setq vertico-resize t)

    ;; Enable cycling for `vertico-next' and `vertico-previous'.
    (setq vertico-cycle t))

  ;; Use the `orderless' completion style.
  (use-package orderless
    :ensure t

    :config
    (setq completion-styles '(orderless basic)
          completion-category-overrides '((file (styles partial-completion)))))

  ;; Enable rich annotations using the Marginalia package.
  (use-package marginalia
    :ensure t

    :config
    (marginalia-mode +1))

  ;; Provides search and navigation based on completing-read.
  (use-package consult
    :ensure t

    :bind
    ;; C-c bindings (mode-specific-map)
    (("C-c M-x" . consult-mode-command)
     ("C-c h" . consult-history)
     ("C-c k" . consult-kmacro)
     ("C-c m" . consult-man)
     ("C-c i" . consult-info)
     ([remap Info-search] . consult-info)
     ;; C-x bindings (ctl-x-map)
     ("C-x M-:" . consult-complex-command)     ;; orig. repeat-complex-command
     ("C-x b" . consult-buffer)                ;; orig. switch-to-buffer
     ("C-x 4 b" . consult-buffer-other-window) ;; orig. switch-to-buffer-other-window
     ("C-x 5 b" . consult-buffer-other-frame)  ;; orig. switch-to-buffer-other-frame
     ("C-x r b" . consult-bookmark)            ;; orig. bookmark-jump
     ("C-x p b" . consult-project-buffer)      ;; orig. project-switch-to-buffer
     ;; Custom M-# bindings for fast register access
     ("M-#" . consult-register-load)
     ("M-'" . consult-register-store)          ;; orig. abbrev-prefix-mark (unrelated)
     ("C-M-#" . consult-register)
     ;; Other custom bindings
     ("M-y" . consult-yank-pop)                ;; orig. yank-pop
     ;; M-g bindings (goto-map)
     ("M-g e" . consult-compile-error)
     ("M-g f" . consult-flymake)               ;; Alternative: consult-flycheck
     ("M-g g" . consult-goto-line)             ;; orig. goto-line
     ("M-g M-g" . consult-goto-line)           ;; orig. goto-line
     ("M-g o" . consult-outline)               ;; Alternative: consult-org-heading
     ("M-g m" . consult-mark)
     ("M-g k" . consult-global-mark)
     ("M-g i" . consult-imenu)
     ("M-g I" . consult-imenu-multi)
     ;; M-s bindings (search-map)
     ("M-s d" . consult-find)
     ("M-s D" . consult-locate)
     ("M-s g" . consult-grep)
     ("M-s G" . consult-git-grep)
     ("M-s r" . consult-ripgrep)
     ("M-s l" . consult-line)
     ("M-s L" . consult-line-multi)
     ("M-s k" . consult-keep-lines)
     ("M-s u" . consult-focus-lines)
     ;; Isearch integration
     ("M-s e" . consult-isearch-history)
     :map isearch-mode-map
     ("M-e" . consult-isearch-history)         ;; orig. isearch-edit-string
     ("M-s e" . consult-isearch-history)       ;; orig. isearch-edit-string
     ("M-s l" . consult-line)                  ;; needed by consult-line to detect isearch
     ("M-s L" . consult-line-multi)            ;; needed by consult-line to detect isearch
     ;; Minibuffer history
     :map minibuffer-local-map
     ("M-s" . consult-history)                 ;; orig. next-matching-history-element
     ("M-r" . consult-history))                ;; orig. previous-matching-history-element

    ;; Enable automatic preview at point in the *Completions* buffer. This is
    ;; relevant when you use the default completion UI.
    :hook
    (completion-list-mode . consult-preview-at-point-mode)

    :init
    ;; Optionally configure the register formatting. This improves the register
    ;; preview for `consult-register', `consult-register-load',
    ;; `consult-register-store' and the Emacs built-ins.
    (setq register-preview-delay 0.5
          register-preview-function #'consult-register-format)

    ;; Optionally tweak the register preview window.
    ;; This adds thin lines, sorting and hides the mode line of the window.
    (advice-add #'register-preview :override #'consult-register-window)

    ;; Use Consult to select xref locations with preview
    (setq xref-show-xrefs-function #'consult-xref
          xref-show-definitions-function #'consult-xref))

  ;; Convenient jumping between windows.
  (use-package ace-window
    :ensure t

    :bind
    (([remap other-window] . ace-window)
     ("s-w" . ace-window)))

  ;; Convenient jumping to text.
  (use-package avy
    :ensure t

    :bind
    (("C-:" . avy-goto-char)
     ("s-," . avy-goto-char)
     ("C-'" . avy-goto-char-2)
     ("M-g f" . avy-goto-line)
     ("M-g w" . avy-goto-word-1)
     ("M-g e" . avy-goto-word-0)
     ("s-." . avy-goto-word-or-subword-1)
     ("C-c v" . avy-goto-word-or-subword-1)))

  ;;
  ;; History
  ;;

  (defvar savefile-dir (expand-file-name "savefile" user-emacs-directory)
    "This folder stores all the automatically generated save/history-files.")
  (unless (file-exists-p savefile-dir)
    (make-directory savefile-dir))

  ;; Persist history over Emacs restarts. Vertico sorts by history position.
  (use-package savehist
    :config
    (setq savehist-additional-variables
          ;; Search entries.
          '(search-ring regexp-search-ring)
          ;; Save every minute.
          savehist-autosave-interval 60
          ;; Keep the home clean.
          savehist-file (expand-file-name "savehist" savefile-dir))

    (savehist-mode +1))

  ;; Remember your location in a file when saving files.
  (use-package saveplace
    :config
    (setq save-place-file (expand-file-name "saveplace" savefile-dir))

    (save-place-mode +1))

  ;; Save recent files.
  (use-package recentf
    :config
    (setq recentf-save-file (expand-file-name "recentf" savefile-dir)
          recentf-max-saved-items 500
          recentf-max-menu-items 15
          ;; Disable recentf-cleanup on Emacs start, because it can cause
          ;; problems with remote files.
          recentf-auto-cleanup 'never)

    (recentf-mode +1))

  ;; Supercharge your undo/redo with undo-tree.
  (use-package undo-tree
    :ensure t

    :config
    ;; Autosave the undo-tree history.
    (setq undo-tree-history-directory-alist `((".*" . ,temporary-file-directory))
          undo-tree-auto-save-history t)

    (global-undo-tree-mode +1))

  ;;
  ;; Project Management
  ;;

  (use-package magit
    :ensure t

    :bind
    (("C-x g" . magit)))

  ;;
  ;; Text Assistance
  ;;

  (setq-default indent-tabs-mode nil)   ;; Don't use tabs to indent,
  (setq-default tab-width 8)            ;; but maintain correct appearance.

  ;; Newline at end of file.
  (setq require-final-newline t)

  ;; Maintain balanced parens.
  (use-package smartparens
    :ensure t
    :hook prog-mode

    :config
    (require 'smartparens-config)

    (setq sp-base-key-bindings 'paredit
          sp-autoskip-closing-pair 'always
          sp-hybrid-kill-entire-symbol nil)

    (sp-use-paredit-bindings))

  (use-package flyspell
    :hook
    ((text-mode . flyspell-mode)
     (prog-mode . flyspell-prog-mode))

    :config
    (setq ispell-program-name "aspell" ; use aspell instead of ispell
          ispell-extra-args '("--sug-mode=ultra")))

  ;;
  ;; Text Expansion
  ;;

  (use-package company
    :ensure t

    :config
    (global-company-mode +1))

  ;;
  ;; Text Manipulation
  ;;

  (use-package wgrep
    :ensure t

    :bind
    (:map grep-mode-map
          ("C-c C-p" . wgrep-change-to-wgrep-mode)))

  (use-package selected
    :ensure t

    :config
    (selected-global-mode +1))

  (use-package multiple-cursors
    :ensure t

    :bind
    (:map selected-keymap
          ("C-x c" . mc/edit-lines)))

  ;;
  ;; Shells
  ;;

  (use-package eshell
    :bind
    (("C-x m" . eshell)
     ("C-x M" . (lambda () (interactive) (eshell t))))

    :config
    (setq eshell-directory-name (expand-file-name "eshell" savefile-dir)))

  ;;
  ;; Coding Assistance
  ;;

  (use-package eglot
    :hook ((go-ts-mode) . eglot-ensure)

    :bind
    (:map eglot-mode-map
          ("C-c C-l ." . xref-find-definitions)
          ("C-c C-l ?" . xref-find-references)
          ("C-c C-l r" . eglot-rename)
          ("C-c C-l i" . eglot-find-implementation)
          ("C-c C-l d" . eldoc)
          ("C-c C-l e" . eglot-code-actions))

    :config
    (defun eglot-code-action-organize-imports-interactive ()
      (eglot-code-actions nil nil "source.organizeImports" t)))

  (use-package realgud
    :ensure t
    :defer t

    :init
    (defvar realgud-alist
      '((realgud:bashdb    :modes (sh-mode bash-ts-mode))
        (realgud:gdb)
        (realgud:gub       :modes (go-mode go-ts-mode))
        (realgud:kshdb     :modes (sh-mode bash-ts-mode))
        (realgud:pdb       :modes (python-mode python-ts-mode))
        (realgud:perldb    :modes (perl-mode))
        (realgud:rdebug    :modes (ruby-mode ruby-ts-mode))
        (realgud:remake)
        (realgud:trepan    :modes (perl-mode))
        (realgud:trepan2   :modes (python-mode python-ts-mode))
        (realgud:trepan3k  :modes (python-mode python-ts-mode))
        (realgud:trepanjs  :modes (javascript-mode js2-mode js3-mode js-ts-mode))
        (realgud:trepanpl  :modes (perl-mode))
        (realgud:zshdb     :modes (sh-mode bash-ts-mode))))

    ;; Realgud doesn't generate its autoloads properly so we do it ourselves
    (dolist (debugger realgud-alist)
      (autoload (car debugger)
        (if-let (sym (plist-get (cdr debugger) :package))
            (symbol-name sym)
          "realgud")
        nil t)))

  (use-package chatgpt-shell
    :commands
    (chatgpt-shell)

    :config
    (setq chatgpt-shell-openai-key (lambda ()
                                     (nth 0 (process-lines "pass" "show" "openai-key")))))

  (use-package helpful
    :ensure t

    :bind
    (("C-h f" . helpful-callable)
     ("C-h v" . helpful-variable)
     ("C-h k" . helpful-key)
     ("C-h x" . helpful-command)
     ("C-c C-d" . helpful-at-point)))

  ;;
  ;; Coding Language Support
  ;;

  (use-package go-mode
    :ensure t
    :mode "\\.go\\'"

    :init
    ;; Open go files with tree-sitter support.
    (add-to-list 'major-mode-remap-alist
                 '(go-mode . go-ts-mode))

    ;; Set up syntax highlighting for go-ts-mode.
    (defun go-ts-mode-highlighting-setup ()
      (whitespace-toggle-options '(tabs))
      (treesit-font-lock-recompute-features))
    (add-hook 'go-ts-mode-hook #'go-ts-mode-highlighting-setup)

    ;; Set up eglot for go-ts-mode.
    (defun go-ts-mode-before-save-hook-setup ()
      (add-hook 'before-save-hook #'eglot-format-buffer t t)
      (add-hook 'before-save-hook #'eglot-code-action-organize-imports-interactive t t))
    (add-hook 'go-ts-mode-hook #'go-ts-mode-before-save-hook-setup)

    :config
    ;; Prefer goimports to gofmt if installed.
    (let ((goimports (executable-find "goimports")))
      (when goimports
        (setq gofmt-command goimports)))

    ;; CamelCase aware editing operations.
    (subword-mode +1)

    :bind
    (:map go-mode-map
          ("C-c a" . go-test-current-project)
          ("C-c m" . go-test-current-file)
          ("C-c ." . go-test-current-test)
          ("C-c b" . go-run)
          ("C-h f" . godoc-at-point)))

  (use-package nix-mode
    :ensure t
    :mode "\\.nix\\'"))

(provide 'init)
;;; init.el ends here
