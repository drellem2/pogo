;;; pogo.el --- Project navigation, code intelligence -*- lexical-binding: t -*-

;; Copyright © 2022-2032 Daniel Miller <gate46dmiller@gmail.com>

;; Author: Daniel Miller <gate46dmiller@gmail.com>
;; URL: https://github.com/drellem2/pogo
;; Package-Version: 20221022.0000
;; Package-Requires: (
;;     (emacs "25.1")
;;     (request "0.3.2")
;;     (org "9.6.6")
;;     (cl-lib "1.0")
;;     (pcache "0.5.1"))
;; Package-Commit:
;; Keyword: project, convenience, search
;; Version: 0.0.1-snapshot

;; This file is NOT part of GNU Emacs.

;; This program is free software; you can redistribute it and/or modify
;; it under the terms of the GNU General Public License as published by
;; the Free Software Foundation; either version 3, or (at your option)
;; any later version.
;;
;; This program is distributed in the hope that it will be useful,
;; but WITHOUT ANY WARRANTY; without even the implied warranty of
;; MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
;; GNU General Public License for more details.
;;
;; You should have received a copy of the GNU General Public License
;; along with GNU Emacs; see the file COPYING.  If not, write to the
;; Free Software Foundation, Inc., 51 Franklin Street, Fifth Floor,
;; Boston, MA 02110-1301, USA.

;;; Commentary:
;;
;; This library provides project management and code intelligence
;; features via the external process pogod, similar to Language
;; Server Protocol. pogod can be extended with plugins to provide
;; navigation, code search, and more. Currently a project is defined
;; as a git repository with a .git file. See the README for more details.
;;
;;; Code:

(require 'cl-lib)
(require 'org)
(require 'request)
(require 'url)
(require 'pcache)

;;; Variables

(defvar pogo-search-plugin nil)
(defvar pogo-search-plugin-name "pogo-plugin-search")
(defvar pogo-process nil)
(defvar pogo-visit-cache (pcache-repository "pogo-visit-cache"))
(defvar pogo-visit-cache-seconds (* 60 10))
(defvar pogo-failure-count 0) ;; Number of failures starting pogo server
(defvar pogo-server-started nil)
(defvar pogo-commander-methods nil
  "List of file-selection methods for the `pogo-commander' command.
Each element is a list (KEY DESCRIPTION FUNCTION).
DESCRIPTION is a one-line description of what the key selects.")

;;; Set request-message-level
(when (not (eql -1 request-message-level))
  (message "Warning: Setting request-message-level to -1")
  (customize-set-variable 'request-message-level -1))

;;; Customization
(defgroup pogo nil
  "Code intelligence in the background."
  :group 'tools
  :group 'convenience
  :link '(url-link :tag "GitHub" "https://github.com/drellem/pogo")
  :link '(emacs-commentary-link :tag "Commentary" "pogo"))

(defcustom pogo-keymap-prefix nil
  "Pogo keymap prefix."
  :group 'pogo
  :type 'string)

(defcustom pogo-debug-log t
  "Pogo debug logging."
  :group 'pogo
  :type 'boolean)

(defcustom pogo-project-name nil
  "If this value is non-nil, it will be used as project name.

It has precedence over function `pogo-project-name-function'."
  :group 'pogo
  :type 'string
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-project-name-function 'pogo-default-project-name
  "A function that receives the project-root and returns the project name.

If variable `pogo-project-name' is non-nil, this function will not be
used."
  :group 'pogo
  :type 'function
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-buffers-filter-function nil
  "A function used to filter the buffers in `pogo-project-buffers'.

The function should accept and return a list of Emacs buffers.
Two example filter functions are shipped by default -
`pogo-buffers-with-file' and
`pogo-buffers-with-file-or-process'."
  :group 'pogo
  :type 'function)

(defcustom pogo-completion-system 'auto
  "The completion system to be used by Pogo."
  :group 'pogo
  :type '(radio
          (const :tag "Auto-detect" auto)
          (const :tag "Ido" ido)
          (const :tag "Helm" helm)
          (const :tag "Ivy" ivy)
          (const :tag "Default" default)
          (function :tag "Custom function")))

(defcustom pogo-globally-ignored-buffers nil
  "A list of buffer-names ignored by pogo.

You can use either exact buffer names or regular expressions.
If a buffer is in the list pogo will ignore it for
functions working with buffers."
  :group 'pogo
  :type '(repeat string)
  :package-version '(pogo . "0.12.0"))

(defcustom pogo-globally-ignored-modes
  '("erc-mode"
    "help-mode"
    "completion-list-mode"
    "Buffer-menu-mode"
    "gnus-.*-mode"
    "occur-mode")
  "A list of regular expressions for major modes ignored by pogo.

If a buffer is using a given major mode, pogo will ignore
it for functions working with buffers."
  :group 'pogo
  :type '(repeat string))

(defcustom pogo-require-project-root 'prompt
  "Require the presence of a project root to operate when true.
When set to `prompt' Pogo will ask you to select a project
directory if you're not in a project.

When nil Pogo will consider the current directory the project root."
  :group 'pogo
  :type '(choice (const :tag "No" nil)
                 (const :tag "Yes" t)
                 (const :tag "Prompt for project" prompt)))

(defcustom pogo-current-project-on-switch 'remove
  "Determines whether to display current project when switching projects.

When set to `remove' current project is not included, `move-to-end'
will display current project and the end of the list of known
projects, `keep' will leave the current project at the default
position."
  :group 'pogo
  :type '(radio
          (const :tag "Remove" remove)
          (const :tag "Move to end" move-to-end)
          (const :tag "Keep" keep)))

(defcustom pogo-switch-project-action 'pogo-find-file
  "Action invoked after switching projects with `pogo-switch-project'.

Any function that does not take arguments will do."
  :group 'pogo
  :type 'function)

(defcustom pogo-kill-buffers-filter 'kill-all
  "Determine which buffers are killed by `pogo-kill-buffers'.

When the kill-all option is selected, kills each buffer.

When the kill-only-files option is selected, kill only the buffer
associated to a file.

Otherwise, it should be a predicate that takes one argument: the buffer to
be killed."
  :group 'pogo
  :type '(radio
          (const :tag "All project buffers" kill-all)
          (const :tag "Project file buffers" kill-only-files)
          (function :tag "Predicate")))

(defcustom pogo-auto-discover t
  "Whether to discover projects when `pogo-mode' is activated."
  :group 'pogo
  :type 'boolean
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-project-search-path nil
  "List of folders where pogo is automatically going to look for projects.
You can think of something like $PATH, but for projects instead of executables.
Examples of such paths might be ~/projects, ~/work, (~/github . 1) etc.
For elements of form (DIRECTORY . DEPTH), DIRECTORY has to be a
directory and DEPTH an integer that specifies the depth at which to
look for projects.  A DEPTH of 0 means check DIRECTORY.  A depth of 1
means check all the subdirectories of DIRECTORY.  Etc."
  :group 'pogo
  :type '(repeat (choice directory (cons directory (integer :tag "Depth"))))
  :package-version '(pogo . "1.0.0"))

(defcustom pogo-max-file-buffer-count nil
  "Maximum number of file buffers per project that are kept open.

If the value is nil, there is no limit to the opend buffers count."
  :group 'pogo
  :type 'integer
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-dynamic-mode-line t
  "If true, update the mode-line dynamically.
Only file buffers are affected by this, as the update happens via
`find-file-hook'.

See also `pogo-mode-line-function' and `pogo-update-mode-line'."
  :group 'pogo
  :type 'boolean
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-mode-line-function 'pogo-default-mode-line
  "The function to use to generate project-specific mode-line.
The default function adds the project name and type to the mode-line.
See also `pogo-update-mode-line'."
  :group 'pogo
  :type 'function
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-mode-line-prefix
  " Pogo"
  "Mode line lighter prefix for Pogo.
It's used by `pogo-default-mode-line'
when using dynamic mode line lighter and is the only
thing shown in the mode line otherwise."
  :group 'pogo
  :type 'string
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-max-failure-count 3
  "Maximum number of times to attempt to start the pogo server."
  :group 'pogo
  :type 'integer
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-health-check-seconds 10
  "Number of seconds to wait before trying health check."
  :group 'pogo
  :type 'integer
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-track-known-projects-automatically t
  "Controls whether Pogo will automatically register known projects.

When set to nil you'll have always add projects explicitly with
`pogo-add-known-project'."
  :group 'pogo
  :type 'boolean
  :package-version '(pogo . "0.0.1"))


(defconst pogo-version "0.0.1-snapshot"
  "The current version of Pogo.")

(defun pogo--pkg-version ()
  "Extract Pogo's package version from its package metadata."
  ;; Use `cond' below to avoid a compiler unused return value warning
  ;; when `package-get-version' returns nil. See #3181.
  ;; FIXME: Inline the logic from package-get-version and adapt it
  (cond ((fboundp 'package-get-version)
         (package-get-version))))

(defun pogo-log (msg &rest args)
  (when pogo-debug-log
    (progn
      (when (not (get-buffer "*pogo-mode-log*"))
        (get-buffer-create "*pogo-mode-log*"))
      (with-current-buffer "*pogo-mode-log*"
        (goto-char (point-max))
        (insert (apply 'format (append (list (concat msg "\n")) args)))))))

;;;###autoload
(defun pogo-version (&optional show-version)
  "Get the Pogo version as string.

If called interactively or if SHOW-VERSION is non-nil, show the
version in the echo area and the messages buffer.

The returned string includes both, the version from package.el
and the library version, if both a present and different.

If the version number could not be determined, signal an error,
if called interactively, or if SHOW-VERSION is non-nil, otherwise
just return nil."
  (interactive (list t))
  (let ((version (or (pogo--pkg-version) pogo-version)))
    (if show-version
        (pogo-log "Pogo %s" version)
      version)))

;;; Misc utility functions
(defun pogo-difference (list1 list2)
  (cl-remove-if
   (lambda (x) (member x list2))
   list1))

;;; Internal functions

;; Start server
(defun pogo-start ()
  (progn
    (pogo-log "Starting server")
    (when (or (and (not (file-remote-p default-directory))
                   (executable-find "pogod"))
              (and (version<= "27.0" emacs-version)
                   (with-no-warnings
                     (executable-find "pogod"
                                      (file-remote-p
                                       default-directory)))))
      (with-temp-buffer (start-process "pogod" "*pogo-server*" "pogod")))))

;;; Find next/previous project buffer
(defun pogo--repeat-until-project-buffer (orig-fun &rest args)
  "Repeat ORIG-FUN with ARGS until the current buffer is a project buffer."
  (if (pogo-project-root)
      (let* ((other-project-buffers (make-hash-table :test 'eq))
             (pogo-project-buffers (pogo-project-buffers))
             (max-iterations (length (buffer-list)))
             (counter 0))
        (dolist (buffer pogo-project-buffers)
          (unless (eq buffer (current-buffer))
            (puthash buffer t other-project-buffers)))
        (when (cdr-safe pogo-project-buffers)
          (while (and (< counter max-iterations)
                      (not (gethash (current-buffer) other-project-buffers)))
            (apply orig-fun args)
            (cl-incf counter))))
    (apply orig-fun args)))

(defun pogo-next-project-buffer ()
  "In selected window switch to the next project buffer.

If the current buffer does not belong to a project, call `next-buffer'."
  (interactive)
  (pogo--repeat-until-project-buffer #'next-buffer))

(defun pogo-previous-project-buffer ()
  "In selected window switch to the previous project buffer.

If the current buffer does not belong to a project, call `previous-buffer'."
  (interactive)
  (pogo--repeat-until-project-buffer #'previous-buffer))

(defun pogo-maybe-limit-project-file-buffers ()
  "Limit the opened file buffers for a project.
   The function simply kills the last buffer, as it's normally called
   when opening new files."
  (when pogo-max-file-buffer-count
    (let ((project-buffers (pogo-project-buffer-files)))
      (when (> (length project-buffers) pogo-max-file-buffer-count)
        (kill-buffer (car (last project-buffers)))))))

;;;###autoload
(defun pogo-discover-projects-in-directory (directory &optional depth)
  "For now a noop. Eventually we may visit all maximal-depth files in
   the directory."
  ())

;;;###autoload
(defun pogo-discover-projects-in-search-path ()
  "Discover projects in `pogo-project-search-path'.
Invoked automatically when `pogo-mode' is enabled."
  (interactive)
  (dolist (path pogo-project-search-path)
    (if (consp path)
        (pogo-discover-projects-in-directory (car path) (cdr path))
      (pogo-discover-projects-in-directory path 1))))


(defun pogo-recentf-files ()
  "Return a list of recently visited files in a project."
  (and (boundp 'recentf-list)
       (let ((project-root (pogo-acquire-root)))
         (mapcar
          (lambda (f) (file-relative-name f project-root))
          (cl-remove-if-not
           (lambda (f) (string-prefix-p project-root (expand-file-name f)))
           recentf-list)))))

(defun pogo-project-p (&optional dir)
  "Check if DIR is a project.
Defaults to the current directory if not provided
explicitly."
  (pogo-project-root (or dir default-directory)))


(defun pogo-switch-project-by-name (project-to-switch &optional arg)
  "Switch to project by project name PROJECT-TO-SWITCH.
Invokes the command referenced by `pogo-switch-project-action' on switch.
With a prefix ARG invokes `pogo-commander' instead of
`pogo-switch-project-action.'"
  ;; let's make sure that the target directory exists and is actually a project
  ;; we ignore remote folders, as the check breaks for TRAMP unless already connected
  (unless (or (file-remote-p project-to-switch)
              (pogo-project-p project-to-switch))
    (pogo-remove-known-project project-to-switch)
    (error "Directory %s is not a project" project-to-switch))
  (let ((switch-project-action (if arg
                                   'pogo-commander
                                 pogo-switch-project-action)))
    (run-hooks 'pogo-before-switch-project-hook)
    (let* ((default-directory project-to-switch)
           (switched-buffer
            ;; use a temporary buffer to load PROJECT-TO-SWITCH's dir-locals
            ;; before calling SWITCH-PROJECT-ACTION
            (with-temp-buffer
              (hack-dir-local-variables-non-file-buffer)
              ;; Normally the project name is determined from the current
              ;; buffer. However, when we're switching projects, we want to
              ;; show the name of the project being switched to, rather than
              ;; the current project, in the minibuffer.
              (let ((pogo-project-name (funcall pogo-project-name-function
                                                project-to-switch)))
                (funcall switch-project-action)
                (current-buffer)))))
      ;; If switch-project-action switched buffers then with-temp-buffer will
      ;; have lost that change, so switch back to the correct buffer.
      (when (buffer-live-p switched-buffer)
        (switch-to-buffer switched-buffer)))
    (run-hooks 'pogo-after-switch-project-hook)))

(defun pogo--remove-current-project (projects)
  "Remove the current project (if any) from the list of PROJECTS."
  (if-let ((project (pogo-project-root)))
      (pogo-difference projects
                       (list (abbreviate-file-name project)))
    projects))

(defun pogo-relevant-known-projects ()
  "Return a list of known projects."
  (pcase pogo-current-project-on-switch
    ('remove (pogo--remove-current-project (pogo-known-projects)))
    ('move-to-end (pogo--move-current-project-to-end (pogo-known-projects)))
    ('keep (pogo-known-projects))))

(defun pogo-relevant-open-projects ()
  "Return a list of open projects."
  (let ((open-projects (pogo-open-projects)))
    (pcase pogo-current-project-on-switch
      ('remove (pogo--remove-current-project open-projects))
      ('move-to-end (pogo--move-current-project-to-end open-projects))
      ('keep open-projects))))

(defun pogo-ensure-project (dir)
  "Ensure that DIR is non-nil.
Useful for commands that expect the presence of a project.
Controlled by `pogo-require-project-root'.

See also `pogo-acquire-root'."
  (if dir
      dir
    (cond
     ((eq pogo-require-project-root 'prompt) (pogo-completing-read
                                              "Switch to project: "
                                              (pogo-known-projects)))
     (pogo-require-project-root (error
                                 "Pogo cannot find a project definition in %s"
                                 default-directory))
     (t default-directory))))

(defun pogo-ignored-buffer-p (buffer)
  "Check if BUFFER should be ignored.

Regular expressions can be use."
  (or
   (with-current-buffer buffer
     (cl-some
      (lambda (name)
        (string-match-p name (buffer-name)))
      pogo-globally-ignored-buffers))
   (with-current-buffer buffer
     (cl-some
      (lambda (mode)
        (string-match-p (concat "^" mode "$")
                        (symbol-name major-mode)))
      pogo-globally-ignored-modes))))


(defun pogo-project-buffers (&optional project)
  "Get a list of a project's buffers.
If PROJECT is not specified the command acts on the current project."
  (let* ((project-root (or project (pogo-acquire-root)))
         (test-out (pogo-log "project-root is %s" project-root))
         (all-buffers (cl-remove-if-not
                       (lambda (buffer)
                         (pogo-project-buffer-p buffer project-root))
                       (buffer-list))))
    (if pogo-buffers-filter-function
        (funcall pogo-buffers-filter-function all-buffers)
      all-buffers)))

(defun pogo-project-buffer-p (buffer project-root)
  "Check if BUFFER is under PROJECT-ROOT."
  (with-current-buffer buffer
    (let ((directory (if buffer-file-name
                         (file-name-directory buffer-file-name)
                       default-directory)))
      (string= (pogo-visit directory) (expand-file-name project-root)))))

(defun pogo-project-buffer-names ()
  "Get a list of project buffer names."
  (mapcar #'buffer-name (pogo-project-buffers)))

(defun pogo-read-buffer-to-switch (prompt)
  "Read the name of a buffer to switch to, prompting with PROMPT.

This function excludes the current buffer from the offered
choices."
  (pogo-completing-read
   prompt
   (delete (buffer-name (current-buffer))
           (pogo-project-buffer-names))))

(defun pogo-default-project-name (project-root)
  "Default function used to create the project name.
The project name is based on the value of PROJECT-ROOT."
  (file-name-nondirectory (directory-file-name project-root)))

(defun pogo-project-name (&optional project)
  "Return project name.
If PROJECT is not specified acts on the current project."
  (or pogo-project-name
      (let ((project-root (or project (pogo-project-root))))
        (if project-root
            (funcall pogo-project-name-function project-root)
          "-"))))

(defun pogo-symbol-or-selection-at-point ()
  "Get the symbol or selected text at point."
  (if (use-region-p)
      (buffer-substring-no-properties (region-beginning) (region-end))
    (pogo-symbol-at-point)))

(defun pogo-symbol-at-point ()
  "Get the symbol at point and strip its properties."
  (substring-no-properties (or (thing-at-point 'symbol) "")))

(defun pogo-prepend-project-name (string)
  "Prepend the current project's name to STRING."
  (format "[%s] %s" (pogo-project-name) string))

(cl-defun pogo-completing-read (prompt choices &key initial-input action)
  "Present a project tailored PROMPT with CHOICES."
  (let ((prompt (pogo-prepend-project-name prompt))
        res)
    (setq res
          (pcase (if (eq pogo-completion-system 'auto)
                     (cond
                      ((bound-and-true-p ido-mode)  'ido)
                      ((bound-and-true-p helm-mode) 'helm)
                      ((bound-and-true-p ivy-mode)  'ivy)
                      (t 'default))
                   pogo-completion-system)
            ('default (completing-read prompt choices nil nil initial-input))
            ('ido (ido-completing-read prompt choices nil nil initial-input))
            ('helm
             (if (and (fboundp 'helm)
                      (fboundp 'helm-make-source))
                 (helm :sources
                       (helm-make-source "Pogo" 'helm-source-sync
                                         :candidates choices
                                         :action (if action
                                                     (prog1 action
                                                       (setq action nil))
                                                   #'identity))
                       :prompt prompt
                       :input initial-input
                       :buffer "*helm-pogo*")
               (user-error "Please install helm")))
            ('ivy
             (if (fboundp 'ivy-read)
                 (ivy-read prompt choices
                           :initial-input initial-input
                           :action (prog1 action
                                     (setq action nil))
                           :caller 'pogo-completing-read)
               (user-error "Please install ivy")))
            (_ (funcall pogo-completion-system prompt choices))))
    (if action
        (funcall action res)
      res)))

(defun pogo--read-search-string-with-default (prefix-label)
  (let* ((prefix-label (pogo-prepend-project-name prefix-label))
         (default-value (pogo-symbol-or-selection-at-point))
         (default-label (if (or (not default-value)
                                (string= default-value ""))
                            ""
                          (format " (default %s)" default-value))))
    (read-string (format "%s%s: " prefix-label default-label) nil nil
                 default-value)))

(defun pogo--search-compare (fst snd)
  "Returns true when fst has more matching lines than snd"
  (let ((lst (mapcar (lambda (e) (length (cdr (assoc #'matches e))))
                     (list fst snd))))
    (> (car lst) (cadr lst))))

(defun pogo--delimit (e l)
  "Delimits the list l with the element e."
  (if (or (null l) (null (cdr l)))
      l
    (cons (car l)
          (cons
           e
           (pogo--delimit e (cdr l))))))

(defun pogo--search (&optional query arg)
  "Search a project."
  (interactive "P")
  (let* ((project-root (pogo-acquire-root))
         (search-query (or query (pogo--read-search-string-with-default
                                  "Zoekt query:"))))
    (progn
      (get-buffer-create "*search*")
      (with-current-buffer "*search*"
        (let ((buffer-name (concat "*search <" project-root ":" search-query
                                   ">*")))
          (progn
            (rename-buffer buffer-name)
            (let* ((original-resp (list (car
                                         (pogo-project-search project-root
                                                              search-query))))
                   (files (cdr (assoc 'files original-resp)))
                   (sorted-files
                    (pogo-print-and-return "Sorted files: "
                                           (to-list
                                            (sort
                                             files
                                             #'pogo--search-compare))))
                   (file-paths
                    (pogo-print-and-return "file-paths: "
                                           (mapcar (lambda (e)
                                                     (cdr (assoc 'path e)))
                                                   sorted-files)))
                   (org-format-files
                    (pogo-print-and-return "org-format-files: "
                                           (mapcar (lambda (s)
                                                     (concat "[[" s "][" s
                                                             "]]"))
                                                   file-paths)))
                   (files-with-newlines (pogo--delimit "\n" org-format-files))
                   (results (reduce #'concat files-with-newlines)))
              (insert (if (null results) "nil" results)))
            (org-mode)
            (switch-to-buffer buffer-name)))))))

(defun pogo--find-file (&optional ff-variant)
  "Jump to a project's file using completion.
With FF-VARIANT set to a
defun, use that instead of `find-file'.   A typical example of such a defun
would be `find-file-other-window' or `find-file-other-frame'"
  (interactive "P")
  (let* ((project-root (pogo-acquire-root))
         (file (pogo-completing-read "Find file: "
                                     (pogo-project-files project-root)))
         (ff (or ff-variant #'find-file)))
    (when file
      (funcall ff (expand-file-name file project-root))
      (run-hooks 'pogoc-find-file-hook))))

;; External functions

(defun pogo-try-start ()
  "Attempt to start the pogo server, run health checks."
  (interactive)
  (setenv "POGO_HOME" (expand-file-name "~"))
  (setenv "POGO_PLUGIN_PATH" (concat
                              (string-remove-suffix "pogod"
                                                    (executable-find "pogod"))
                              "plugin"))
  (when (not pogo-server-started)
    (if (>= pogo-failure-count pogo-max-failure-count)
        (message
         "Error starting pogo server. Try again with M-x pogo-try-start")
      (progn
        (setq pogo-process (pogo-start))
        (run-with-timer pogo-health-check-seconds nil 'pogo-health-check)))))

(defun pogo-health-check ()
  (request "http://localhost:10000/health"
    :success (cl-function (lambda (&key data &allow-other-keys)
                            (setq pogo-failure-count 0)
                            (setq pogo-server-started t)
                            (pogo-log "Health check success")))
    :error (cl-function (lambda (&key error-thrown &allow-other-keys)
                          (setq pogo-server-started nil)
                          (setq pogo-failure-count (+ pogo-failure-count 1))
                          (pogo-log "Health check failed %s" error-thrown)
                          (pogo-try-start)))))

(defun pogo-check-live ()
  "Make sure the process is still alive."
  (when (not (process-live-p pogo-process))
    (pogo-try-start)))

(defun pogo-known-projects ()
  (let ((resp (request-response-data (request "http://localhost:10000/projects"
                                       :sync t
                                       :parser 'json-read
                                       :success (cl-function
                                                 (lambda
                                                   (&key data
                                                         &allow-other-keys)
                                                   (pogo-log "Received: %s"
                                                             data)))
                                       :error (cl-function
                                               (lambda
                                                 (&key error-thrown
                                                       &allow-other-keys)
                                                 (pogo-log
                                                  "Error getting projects: %s"
                                                  error-thrown)
                                                 (pogo-check-live)))))))
    (mapcar (lambda (x) (cdr (assoc 'path x))) resp)))



(defun pogo-open-projects ()
  "Return a list of all open projects.
An open project is a project with any open buffers."
  (delete-dups
   (delq nil
         (mapcar (lambda (buffer)
                   (with-current-buffer buffer
                     (when (pogo-project-p)
                       (abbreviate-file-name (pogo-project-root)))))
                 (buffer-list)))))

(defun pogo-visit (relative-path)
  (let* ((path (expand-file-name relative-path))
        (path-sym (intern path)))
    (or
     (pcache-get pogo-visit-cache path-sym)
     (progn
       (let* ((nillable-result (pogo-visit-call relative-path))
             (result (or nillable-result "")))
         (progn
           (pcache-put pogo-visit-cache path-sym result pogo-visit-cache-seconds)
           (pogo-log "Had to place response in cache for %s %s" path result)
           result))))))

(defun pogo-visit-call (path)
  "Path must be absolute"
  (progn
    (pogo-log "Visiting %s" path)
    (cdr (assoc 'path
                (assoc 'project
                       (request-response-data
                        (request "http://localhost:10000/file"
                          :sync t
                          :type "POST"
                          :data (json-encode
                                 `(("path" . ,path)))
                          :parser 'json-read
                          :success (cl-function
                                    (lambda
                                      (&key data
                                            &allow-other-keys)
                                      (pogo-log
                                       "Received: %S" data)))
                          :error (cl-function
                                  (lambda
                                    (&key error-thrown
                                          &allow-other-keys)
                                    (progn
                                      (pogo-log
                                     "Error visiting file %s: %s"
                                     path error-thrown)
                                      (pogo-check-live)))))))))))

(defun pogo-get-search-plugin-path ()
  (if pogo-search-plugin
      pogo-search-plugin
    (letrec
        ((resp
          (request-response-data
           (request "http://localhost:10000/plugins"
             :sync t
             :parser 'json-read
             :success (cl-function (lambda (&key data &allow-other-keys)
                                     (pogo-log "Received: %S" data)))
             :error (cl-function
                     (lambda (&key error-thrown &allow-other-keys)
                       (pogo-log "Error getting search plugin: %s" error-thrown)
                       (pogo-check-live))))))
         (search-plugins (seq-filter
                          (lambda (p)
                            (cl-search pogo-search-plugin-name p))
                          resp))
         (search-plugin (cond
                         ((= 0 (length search-plugins))
                          (progn
                            (pogo-log
                             "Warning: No result found for %s" pogo-search-plugin-name)
                            nil))
                         ((= 1 (length search-plugins)) (car search-plugins))
                         (t (progn
                              (pogo-log
                               "Warning: Found too many results for %s"
                               pogo-search-plugin-name)
                              (car search-plugins))))))
      (progn
        (setq pogo-search-plugin search-plugin)
        search-plugin))))

(defun pogo-print-and-return (msg v)
  (progn
    (pogo-log "%s %s" msg v)
    v))

(defun pogo-nil-or-empty (str)
  (or (not str) (str= "" str)))

(defun to-list (v)
  (append v nil))


(defun pogo-parse-result (resp)
  (let*
      ((decoded (json-read-from-string
                 (url-unhex-string (cdr (assoc 'value resp)))))
       (inner-resp (cdr (assoc 'index decoded)))
       (results (cdr (assoc 'results decoded)))
       (err (cdr (assoc 'error inner-resp)))
       (paths (cdr (assoc 'paths inner-resp))))
    (if (not (pogo-nil-or-empty err))
        (progn
          (pogo-log "Error parsing results %s" err)
          nil)
      (let ((al '()))
        (progn
          (push `(paths . ,paths) al)
          (push `(results . ,results) al)
          al)))))

(defun pogo-project-search (path query)
  (let
      ((command (url-hexify-string (json-encode `(("type" . "search")
                                                  ("projectRoot" . ,path)
                                                  ("duration" . "10s")
                                                  ("data" . ,query))))))
    (to-list
     (cdr
      (assoc
       'results
       (pogo-parse-result
        (request-response-data (request "http://localhost:10000/plugin"
                                 :sync t
                                 :type "POST"
                                 :data (json-encode
                                        `(("plugin" .
                                           ,(pogo-get-search-plugin-path))
                                          ("value" . ,command)))
                                 :parser 'json-read
                                 :success (cl-function
                                           (lambda (&key data
                                                         &allow-other-keys)
                                             (pogo-log
                                              "Received: %S" data)))
                                 :error (cl-function
                                         (lambda
                                           (&key error-throw
                                                 &allow-other-keys)
                                           (pogo-log
                                            "Error searching project: %s"
                                            error-thrown)
                                           (pogo-check-live)))))))))))

(defun pogo-project-files (path)
  (let
      ((command (url-hexify-string (json-encode `(("type" . "files")
                                                  ("projectRoot" . ,path))))))
    (to-list
     (cdr
      (assoc
       'paths
       (pogo-parse-result
        (request-response-data
         (request "http://localhost:10000/plugin"
           :sync t
           :type "POST"
           :data (json-encode `(("plugin" . ,(pogo-get-search-plugin-path))
                                ("value" . ,command)))
           :parser 'json-read
           :success (cl-function (lambda (&key data &allow-other-keys)
                                   (pogo-log "Received: %S" data)))
           :error (cl-function
                   (lambda
                     (&key error-throw
                           &allow-other-keys)
                     (pogo-log "Error getting project files: %s" error-thrown)
                     (pogo-check-live)))))))))))

(defun pogo-project-root (&optional dir)
  (let ((dir (or dir default-directory)))
    (pogo-visit dir)))

(defun pogo-acquire-root
    (&optional dir)
  "Find the current project root, and prompts the user for it if that fails.
Starts the search for the project with DIR."
  ;; TODO add pogo-project-ensure
  (pogo-ensure-project (pogo-project-root dir)))

;;;###autoload
(defun pogo-search (&optional)
  "Search a project by regexp."
  (interactive)
  (pogo--search))

;;;###autoload
(defun pogo-find-file (&optional)
  "Jump to a project's file using completion."
  (interactive)
  (pogo--find-file))

;;;###autoload
(defun pogo-switch-to-buffer ()
  "Switch to a project buffer."
  (interactive)
  (switch-to-buffer
   (pogo-read-buffer-to-switch "Switch to buffer: ")))

;;;###autoload
(defun pogo-dired ()
  "Open `dired' at the root of the project."
  (interactive)
  (dired (pogo-acquire-root)))

;;;###autoload
(defun pogo-switch-project (&optional arg)
  "Switch to a project we have visited before.
Invokes the command referenced by `pogo-switch-project-action' on switch.
With a prefix ARG invokes `pogo-commander' instead of
`pogo-switch-project-action.'"
  (interactive "P")
  (let ((projects (pogo-relevant-known-projects)))
    (if projects
        (pogo-completing-read
         "Switch to project: " projects
         :action (lambda (project)
                   (pogo-switch-project-by-name project arg)))
      (user-error "There are no known projects"))))

;;;###autoload
(defun pogo-switch-open-project (&optional arg)
  "Switch to a project we have currently opened.
Invokes the command referenced by `pogo-switch-project-action' on switch.
With a prefix ARG invokes `pogo-commander' instead of
`pogo-switch-project-action.'"
  (interactive "P")
  (let ((projects (pogo-relevant-open-projects)))
    (if projects
        (pogo-completing-read
         "Switch to open project: " projects
         :action (lambda (project)
                   (pogo-switch-project-by-name project arg)))
      (user-error "There are no open projects"))))

;;;###autoload
(defun pogo-kill-buffers ()
  "Kill project buffers.

The buffer are killed according to the value of
`pogo-kill-buffers-filter'."
  (interactive)
  (let* ((project (pogo-acquire-root))
         (project-name (pogo-project-name project))
         (buffers (pogo-project-buffers project)))
    (when (yes-or-no-p
           (format "Are you sure you want to kill %s buffers for '%s'? "
                   (length buffers) project-name))
      (dolist (buffer buffers)
        (when (and
               ;; we take care not to kill indirect buffers directly
               ;; as we might encounter them after their base buffers are killed
               (not (buffer-base-buffer buffer))
               (if (functionp pogo-kill-buffers-filter)
                   (funcall pogo-kill-buffers-filter buffer)
                 (pcase pogo-kill-buffers-filter
                   ('kill-all t)
                   ('kill-only-files (buffer-file-name buffer))
                   (_ (user-error "Invalid pogo-kill-buffers-filter value: %S"
                                  pogo-kill-buffers-filter)))))
          (kill-buffer buffer))))))

;;;###autoload
(defun pogo-recentf ()
  "Show a list of recently visited files in a project."
  (interactive)
  (if (boundp 'recentf-list)
      (find-file (pogo-expand-root
                  (pogo-completing-read
                   "Recently visited files: "
                   (pogo-recentf-files))))
    (pogo-log"recentf is not enabled")))

;; Bindings

(defmacro def-pogo-commander-method (key description &rest body)
  "Define a new `pogo-commander' method.

KEY is the key the user will enter to choose this method.

DESCRIPTION is a one-line sentence describing how the method.

BODY is a series of forms which are evaluated when the find
is chosen."
  (let ((method `(lambda ()
                   ,@body)))
    `(setq pogo-commander-methods
           (cl-sort (copy-sequence
                     (cons (list ,key ,description ,method)
                           (assq-delete-all ,key pogo-commander-methods)))
                    (lambda (a b) (< (car a) (car b)))))))

(defun pogo-commander-bindings ()
  "Setup the keybindings for the Pogo Commander."

  (def-pogo-commander-method ?g
    "Search project."
    (pogo-search))
  
  (def-pogo-commander-method ?f
    "Find file in project."
    (pogo-find-file))
  
  (def-pogo-commander-method ?b
    "Switch to project buffer."
    (pogo-switch-to-buffer))

  (def-pogo-commander-method ?D
    "Open project root in dired."
    (pogo-dired))
  
  (def-pogo-commander-method ?s
    "Switch project."
    (pogo-switch-project))

  (def-pogo-commander-method ?k
    "Kill all project buffers."
    (pogo-kill-buffers))

  (def-pogo-commander-method ?e
    "Find recently visited file in project."
    (pogo-recentf)))

(defun pogo-update-mode-line ()
  "Update the Pogo mode-line."
  (let ((mode-line (funcall pogo-mode-line-function)))
    (setq pogo--mode-line mode-line))
  (force-mode-line-update))

(defcustom pogo-mode-line-function 'pogo-default-mode-line
  "The function to use to generate project-specific mode-line.
The default function adds the project name and type to the mode-line.
See also `pogo-update-mode-line'."
  :group 'pogo
  :type 'function
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-mode-line-prefix
  " Pogo"
  "Mode line lighter prefix for Pogo.
It's used by `pogo-default-mode-line'
when using dynamic mode line lighter and is the only
thing shown in the mode line otherwise."
  :group 'pogo
  :type 'string
  :package-version '(pogo . "0.0.1"))

(defcustom pogo-track-known-projects-automatically t
  "Controls whether Pogo will automatically register known projects.

When set to nil you'll have always add projects explicitly with
`pogo-add-known-project'."
  :group 'pogo
  :type 'boolean
  :package-version '(pogo . "0.0.1"))

(defun pogo-default-mode-line ()
  "Report project name and type in the modeline."
  (let ((project-name (pogo-project-name))
        (project-type nil))
    (format "%s[%s%s]"
            pogo-mode-line-prefix
            (or project-name "-")
            (if project-type
                (format ":%s" project-type)
              ""))))

(defvar pogo-command-map
  (let ((map (make-sparse-keymap)))
    (define-key map (kbd "g") #'pogo-search)
    (define-key map (kbd "f") #'pogo-find-file)
    (define-key map (kbd "b") #'pogo-switch-to-buffer)
    (define-key map (kbd "D") #'pogo-dired)
    (define-key map (kbd "p") #'pogo-switch-project)
    (define-key map (kbd "k") #'pogo-kill-buffers)
    (define-key map (kbd "e") #'pogo-recentf)
    (define-key map (kbd "<left>") #'pogo-previous-project-buffer)
    (define-key map (kbd "<right>") #'pogo-next-project-buffer)
    map)
  "Keymap for Pogo commands after `pogo-keymap-prefix'.")
(fset 'pogo-command-map pogo-command-map)

(defvar pogo-mode-map
  (let ((map (make-sparse-keymap)))
    (when pogo-keymap-prefix
      (define-key map pogo-keymap-prefix 'pogo-command-map))
    (easy-menu-define pogo-mode-menu map
      "Menu for Pogo"
      '("Pogo" :visible pogo-show-menu
        ("Find..."
         ["Find file" pogo-find-file]
         
         ;; TODO ["Find file in known projects"
         ;; pogo-find-file-in-known-projects]
         )
        ("Buffers"
         ["Switch to buffer" pogo-switch-to-buffer]
         ["Kill project buffers" pogo-kill-buffers]
         ["Recent files" pogo-recentf]
         ["Previous buffer" pogo-previous-project-buffer]
         ["Next buffer" pogo-next-project-buffer])
        ("Projects"
         ["Open project in dired" pogo-dired]
         "--"
         ["Switch to project" pogo-switch-project]
         ["Switch to open project" pogo-switch-open-project])
        ;; TODO Add search group
        "--"
        ["About" pogo-version]))
    map)
  "Keymap for Pogo mode.")

;;; Hooks
(defun pogo-find-file-hook-function ()
  "Called by `find-file-hook' when `pogo-mode' is on.

The function does pretty much nothing when triggered on remote files
as all the operations it normally performs are extremely slow over
tramp."
  (pogo-maybe-limit-project-file-buffers)
  (pogo-project-p))


;;;###autoload
(define-minor-mode pogo-mode
  "Minor mode for project management and code intelligence.
\\{pogo-mode-map}"
  :lighter "pogo" ;; TODO: Make this dynamic
  :keymap pogo-mode-map
  :group 'pogo
  :require 'pogo
  :global t
  (cond
   (pogo-mode
    ;; setup the commander bindings
    (pogo-commander-bindings)
    (when pogo-auto-discover
      (pogo-discover-projects-in-search-path))
    (add-hook 'find-file-hook 'pogo-find-file-hook-function)
    (add-hook 'pogo-find-dir-hook #'pogo-track-known-projects-find-file-hook t)
    (add-hook 'dired-before-readin-hook
              #'pogo-track-known-projects-find-file-hook t t)
    (advice-add 'compilation-find-file :around
                #'compilation-find-file-pogo-find-compilation-buffer)
    (setq pogo-failure-count 0)
    (setq pogo-server-started nil)
    (pogo-try-start))
   (t
    (remove-hook 'find-file-hook #'pogo-find-file-hook-function)
    (remove-hook 'dired-before-readin-hook
                 #'pogo-track-known-projects-find-file-hook t)
    (advice-remove 'compilation-find-file
                   #'compilation-find-file-pogo-find-compilation-buffer))))

(provide 'pogo)

;;; pogo.el ends here
