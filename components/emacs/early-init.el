;;; early-init.el --- Early Init File -*- lexical-binding: t -*-

;; This file is not part of GNU Emacs.

;;; Commentary:

;;; License:

;; This program is free software; you can redistribute it and/or
;; modify it under the terms of the GNU General Public License
;; as published by the Free Software Foundation; either version 3
;; of the License, or (at your option) any later version.
;;
;; This program is distributed in the hope that it will be useful,
;; but WITHOUT ANY WARRANTY; without even the implied warranty of
;; MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
;; GNU General Public License for more details.
;;
;; You should have received a copy of the GNU General Public License
;; along with this program. If not, see <https://www.gnu.org/licenses/>.

;;; Code:

(defvar temp--gc-cons-threshold gc-cons-threshold)
(setq gc-cons-threshold most-positive-fixnum)

(defvar temp--file-name-handler-alist file-name-handler-alist)
(setq file-name-handler-alist nil)

(defvar temp--vc-handled-backends vc-handled-backends)
(setq vc-handled-backends nil)

(add-hook 'emacs-startup-hook
          (lambda ()
            (setq gc-cons-threshold temp--gc-cons-threshold
                  file-name-handler-alist temp--file-name-handler-alist
                  vc-handled-backends temp--vc-handled-backends)))

;;; early-init.el ends here
