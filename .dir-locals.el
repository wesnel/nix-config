;;; Directory Local Variables            -*- no-byte-compile: t -*-
;;; For more information see (info "(emacs) Directory Variables")

;; Runs the assistant in a micro-VM with no route off this machine: the only
;; opening is to the model server running here, so a hosted provider is not
;; something it could reach rather than something it is asked to avoid.
;;
;; Add `--allow-host' entries to reach one deliberately.
((nil . ((eca-custom-command
          . ("eca-sandbox"
             "--image" "eca:latest"
             "--http-map" "ollama:11434=127.0.0.1:11434"
             "--http-map" "docs:6280=127.0.0.1:6280"
             "--env" "OLLAMA_API_URL=http://ollama:11434"))))
 (nix-mode . ((eglot-workspace-configuration . (:nil (:formatting (:command ["alejandra"])))))))
