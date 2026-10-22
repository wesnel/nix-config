;;; inert.el --- check that a review record's text forms no live Org syntax  -*- lexical-binding: t; -*-

;; Run as: emacs -Q --batch -l inert.el FILE BLOCKS ITEMS LINKS
;;
;; FILE is a record that TestInert renders from a corpus of titles and
;; texts made of Org syntax.  Org parses it here, and every element and
;; object must be one the renderer writes itself: its headlines, its two
;; keywords, property drawers, BLOCKS example blocks holding the texts,
;; ITEMS list items (a chunk's hunks) and LINKS file links, each a
;; file:PATH::LINE@SHA citation, and verbatim markup holding a hunk's path
;; that no link can.  Any other link, inline source block or
;; call, macro, export snippet, drawer, table, keyword or headline means
;; text escaped its block or property.  So does a property value holding
;; "[[", or file variables Emacs would read.  Exits 1 on a failure.

(require 'org)
(require 'org-element)

(defconst inert-types
  '(org-data section headline keyword property-drawer node-property
    example-block plain-list item paragraph link verbatim)
  "The element and object types the renderer writes.")

(defconst inert-headlines
  '("Review" "Pull request" "Walkthrough" "Diagrams" "Findings" "Rounds"
    "Outcome" "Unverified" "Dropped" "Draft")
  "The renderer's own headlines, besides those naming an ID.")

(defconst inert-id "\\`\\(?:[CDFR][0-9]+\\|T-[A-Za-z0-9_=-]+\\)\\'"
  "The IDs the corpus's headlines carry.")

(defun inert-check (file blocks items links)
  "Return the failures found in FILE, a list of strings."
  (with-temp-buffer
    (insert-file-contents file)
    (org-mode)
    (let ((tree (org-element-parse-buffer))
          (failures nil)
          (counts (list 0 0 0)))
      (org-element-map tree (append '(org-data) org-element-all-elements org-element-all-objects)
        (lambda (e)
          (let ((type (org-element-type e)))
            (cond
             ;; A thread's node ID, such as PRRT_kwDO..., reads as a
             ;; subscript in its headline, and a path such as x_test.go
             ;; in a link's description: markup alone.
             ((and (memq type '(subscript superscript))
                   (or (org-element-lineage e '(link))
                       (string-prefix-p "T-" (or (org-element-property
                                                  :raw-value (org-element-lineage e '(headline)))
                                                 "")))))
             ((not (memq type inert-types))
              (push (format "%s: %S" type (buffer-substring-no-properties
                                           (org-element-property :begin e)
                                           (org-element-property :end e)))
                    failures))
             ((and (eq type 'keyword)
                   (not (member (org-element-property :key e) '("TITLE" "TODO"))))
              (push (format "keyword %s" (org-element-property :key e)) failures))
             ((eq type 'headline)
              (let ((title (org-element-property :raw-value e))
                    (todo (org-element-property :todo-keyword e)))
                (unless (or (member title inert-headlines) (string-match-p inert-id title))
                  (push (format "headline %S" title) failures))
                (when (and todo (not (member todo '("OPEN" "DEFER" "OPERATOR" "FIX" "PROPOSE"
                                                    "REBUT" "DUPLICATE" "BELONGS"))))
                  (push (format "keyword %S on a headline" todo) failures))))
             ((and (eq type 'node-property)
                   (string-match-p "\\[\\[" (or (org-element-property :value e) "")))
              (push (format "property %s holds a link" (org-element-property :key e)) failures))
             ((eq type 'link)
              (setcar (nthcdr 2 counts) (1+ (nth 2 counts)))
              (unless (and (equal (org-element-property :type e) "file")
                           (string-match-p "\\`[A-Za-z0-9_.@+-][A-Za-z0-9_.@+/ -]*\\'"
                                           (org-element-property :path e))
                           (string-match-p "\\`[0-9]+@[0-9a-f]\\{12\\}\\'"
                                           (or (org-element-property :search-option e) "")))
                (push (format "link %S" (org-element-property :raw-link e)) failures)))
             ((eq type 'item) (setcar (nthcdr 1 counts) (1+ (nth 1 counts))))
             ((eq type 'example-block) (setcar counts (1+ (car counts)))))))
        nil nil nil t)
      (unless (equal counts (list blocks items links))
        (push (format "%S example blocks, items and links, want %S" counts (list blocks items links))
              failures))
      (when (hack-local-variables-prop-line)
        (push "a -*- line sets variables" failures))
      (when (and (fboundp 'hack-local-variables--find-variables)
                 (hack-local-variables--find-variables))
        (push "a Local Variables list sets variables" failures))
      (nreverse failures))))

(let* ((args command-line-args-left)
       (failures (inert-check (nth 0 args)
                              (string-to-number (nth 1 args))
                              (string-to-number (nth 2 args))
                              (string-to-number (nth 3 args)))))
  (setq command-line-args-left nil)
  (if (null failures)
      (princ (format "inert: ok (Org %s)\n" (org-version)))
    (dolist (f failures) (princ (format "inert: %s\n" f)))
    (kill-emacs 1)))

;;; inert.el ends here
