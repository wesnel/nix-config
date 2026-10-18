;;; inert.el --- check that a digest's plan text forms no live Org syntax  -*- lexical-binding: t; -*-

;; Run as: emacs --batch -Q -l inert.el FILE
;;
;; FILE is the standup that TestInert writes to $DIGEST_INERT_OUT for a
;; corpus of plan titles and steps made of Org syntax.  Org parses it here,
;; and every element and object must be one the digest writes itself:
;; headlines of its own sections, its title keyword, its own comment and
;; paragraphs, one list item per plan with one example block holding that
;; plan's text, timestamps, and IDs and hosts verbatim.  Any link, inline
;; source block or call, macro, export snippet, drawer, table, other
;; keyword or headline means plan text escaped its block.  Exits 1 on a
;; failure.

(require 'org)
(require 'org-element)

(defconst inert-types
  '(org-data section headline keyword comment paragraph plain-list item
    example-block timestamp verbatim)
  "The element and object types the digest writes itself.")

(defconst inert-headlines
  '("Plans" "Unchanged" "Pull requests" "Quota" "Plan sync")
  "The digest's own headlines, but the one naming a time.")

(defun inert-check (file)
  "Return the failures found in FILE, a list of strings."
  (with-temp-buffer
    (insert-file-contents file)
    (org-mode)
    (let* ((tree (org-element-parse-buffer))
           (failures nil)
           (items 0)
           (blocks 0))
      (org-element-map tree (append '(org-data) org-element-all-elements org-element-all-objects)
        (lambda (e)
          (let ((type (org-element-type e)))
            (cond
             ((not (memq type inert-types))
              (push (format "%s: %S" type (buffer-substring-no-properties
                                           (org-element-property :begin e)
                                           (org-element-property :end e)))
                    failures))
             ((and (eq type 'keyword)
                   (not (equal (org-element-property :key e) "TITLE")))
              (push (format "keyword %s" (org-element-property :key e)) failures))
             ((and (eq type 'headline)
                   (let ((title (org-element-property :raw-value e)))
                     (not (or (member title inert-headlines)
                              (string-prefix-p "Changed since [" title)))))
              (push (format "headline %S" (org-element-property :raw-value e)) failures))
             ((eq type 'item) (setq items (1+ items)))
             ((eq type 'example-block) (setq blocks (1+ blocks))))))
        nil nil nil t)
      (when (zerop items)
        (push "no plan was listed" failures))
      (unless (= items blocks)
        (push (format "%d items, %d example blocks" items blocks) failures))
      (nreverse failures))))

(let ((failures (inert-check (car command-line-args-left))))
  (setq command-line-args-left nil)
  (if (null failures)
      (princ (format "inert: ok (Org %s)\n" (org-version)))
    (dolist (f failures) (princ (format "inert: %s\n" f)))
    (kill-emacs 1)))

;;; inert.el ends here
