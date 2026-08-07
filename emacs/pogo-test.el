;;; pogo-test.el --- Tests for pogo.el -*- lexical-binding: t -*-

;; Copyright © 2022-2032 Daniel Miller <gate46dmiller@gmail.com>

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

;;; Commentary:
;;
;; ERT tests for pogo.el.
;;
;; Most of these cover pure functions that need no pogo server.  The block at
;; the end is different: it exercises the daemon-start path for real, because
;; the defect it guards (pogo.el spawning a rival pogod beside the one launchd
;; owns) was invisible to every assertion about configuration.  Those tests
;; stand up a real listening socket, spawn a real process, and assert on the
;; operating system's process table.
;;
;; They never touch a real pogod and never touch the daemon port.  The
;; "daemon" is a socket on an ephemeral port; the "pogod" is a throwaway shell
;; script in a temporary directory, put first on `exec-path'.

;;; Code:

(require 'ert)
(require 'cl-lib)
(require 'request)

;; Load pogo.el from the same directory
(let ((dir (file-name-directory (or load-file-name buffer-file-name))))
  (load (expand-file-name "pogo" dir)))

;;; Utility function tests

(ert-deftest pogo-test-nil-or-empty-nil ()
  "Test pogo-nil-or-empty with nil."
  (should (pogo-nil-or-empty nil)))

(ert-deftest pogo-test-nil-or-empty-empty-string ()
  "Test pogo-nil-or-empty with empty string."
  (should (pogo-nil-or-empty "")))

(ert-deftest pogo-test-nil-or-empty-non-empty ()
  "Test pogo-nil-or-empty with non-empty string."
  (should-not (pogo-nil-or-empty "hello")))

(ert-deftest pogo-test-nil-or-empty-whitespace ()
  "Test pogo-nil-or-empty with whitespace (not empty)."
  (should-not (pogo-nil-or-empty " ")))

;;; pogo--to-list

(ert-deftest pogo-test-to-list-vector ()
  "Test pogo--to-list converts a vector to a list."
  (should (equal (pogo--to-list [1 2 3]) '(1 2 3))))

(ert-deftest pogo-test-to-list-empty-vector ()
  "Test pogo--to-list with empty vector."
  (should (equal (pogo--to-list []) nil)))

(ert-deftest pogo-test-to-list-list ()
  "Test pogo--to-list with a list (identity)."
  (should (equal (pogo--to-list '(a b c)) '(a b c))))

;;; pogo--fix-spaces

(ert-deftest pogo-test-fix-spaces-plus ()
  "Test pogo--fix-spaces replaces + with %20."
  (should (equal (pogo--fix-spaces "hello+world") "hello%20world")))

(ert-deftest pogo-test-fix-spaces-no-plus ()
  "Test pogo--fix-spaces with no plus signs."
  (should (equal (pogo--fix-spaces "hello") "hello")))

(ert-deftest pogo-test-fix-spaces-multiple ()
  "Test pogo--fix-spaces with multiple plus signs."
  (should (equal (pogo--fix-spaces "a+b+c") "a%20b%20c")))

(ert-deftest pogo-test-fix-spaces-empty ()
  "Test pogo--fix-spaces with empty string."
  (should (equal (pogo--fix-spaces "") "")))

;;; pogo-difference

(ert-deftest pogo-test-difference-basic ()
  "Test pogo-difference removes elements in list2 from list1."
  (should (equal (pogo-difference '(1 2 3 4) '(2 4)) '(1 3))))

(ert-deftest pogo-test-difference-no-overlap ()
  "Test pogo-difference with no common elements."
  (should (equal (pogo-difference '(1 2) '(3 4)) '(1 2))))

(ert-deftest pogo-test-difference-all-removed ()
  "Test pogo-difference when all elements are removed."
  (should (equal (pogo-difference '(1 2) '(1 2)) nil)))

(ert-deftest pogo-test-difference-empty-list1 ()
  "Test pogo-difference with empty first list."
  (should (equal (pogo-difference '() '(1 2)) nil)))

(ert-deftest pogo-test-difference-empty-list2 ()
  "Test pogo-difference with empty second list."
  (should (equal (pogo-difference '(1 2 3) '()) '(1 2 3))))

;;; pogo--delimit

(ert-deftest pogo-test-delimit-basic ()
  "Test pogo--delimit intersperses element."
  (should (equal (pogo--delimit "," '("a" "b" "c")) '("a" "," "b" "," "c"))))

(ert-deftest pogo-test-delimit-single ()
  "Test pogo--delimit with single element list."
  (should (equal (pogo--delimit "," '("a")) '("a"))))

(ert-deftest pogo-test-delimit-empty ()
  "Test pogo--delimit with empty list."
  (should (equal (pogo--delimit "," nil) nil)))

(ert-deftest pogo-test-delimit-two ()
  "Test pogo--delimit with two elements."
  (should (equal (pogo--delimit "-" '(1 2)) '(1 "-" 2))))

;;; pogo-default-project-name

(ert-deftest pogo-test-default-project-name-basic ()
  "Test pogo-default-project-name extracts directory name."
  (should (equal (pogo-default-project-name "/home/user/projects/myproject/")
                 "myproject")))

(ert-deftest pogo-test-default-project-name-no-trailing-slash ()
  "Test pogo-default-project-name without trailing slash."
  (should (equal (pogo-default-project-name "/home/user/projects/myproject")
                 "myproject")))

(ert-deftest pogo-test-default-project-name-root ()
  "Test pogo-default-project-name with root directory."
  (should (equal (pogo-default-project-name "/") "")))

;;; pogo-url-unhex-string

(ert-deftest pogo-test-url-unhex-basic ()
  "Test pogo-url-unhex-string decodes %XX sequences."
  (should (equal (pogo-url-unhex-string "hello%20world") "hello world")))

(ert-deftest pogo-test-url-unhex-no-encoding ()
  "Test pogo-url-unhex-string with no encoded characters."
  (should (equal (pogo-url-unhex-string "hello") "hello")))

(ert-deftest pogo-test-url-unhex-nil ()
  "Test pogo-url-unhex-string with nil input."
  (should (equal (pogo-url-unhex-string nil) "")))

(ert-deftest pogo-test-url-unhex-multiple ()
  "Test pogo-url-unhex-string with multiple encoded characters."
  (should (equal (pogo-url-unhex-string "a%20b%20c") "a b c")))

;;; pogo--format-chunk

(ert-deftest pogo-test-format-chunk ()
  "Test pogo--format-chunk formats a match chunk."
  (let ((chunk '((line . 42) (content . "foo bar"))))
    (should (equal (pogo--format-chunk chunk) "42: ~foo bar~"))))

;;; pogo--format-file-match

(ert-deftest pogo-test-format-file-match ()
  "Test pogo--format-file-match formats file match as org heading."
  (let ((file-match '((path . "src/main.go")
                      (matches . (((line . 10) (content . "func main")))))))
    (should (string-prefix-p "* [[./src/main.go]"
                             (pogo--format-file-match file-match)))))

;;; pogo--search-compare

(ert-deftest pogo-test-search-compare-more-matches ()
  "Test pogo--search-compare returns t when first has more matches."
  (let ((a `((,#'matches . (1 2 3))))
        (b `((,#'matches . (1)))))
    (should (pogo--search-compare a b))))

(ert-deftest pogo-test-search-compare-fewer-matches ()
  "Test pogo--search-compare returns nil when first has fewer matches."
  (let ((a `((,#'matches . (1))))
        (b `((,#'matches . (1 2 3)))))
    (should-not (pogo--search-compare a b))))

;;; pogo-log

(ert-deftest pogo-test-log-creates-buffer ()
  "Test pogo-log creates the log buffer."
  (let ((pogo-debug-log t))
    (when (get-buffer "*pogo-mode-log*")
      (kill-buffer "*pogo-mode-log*"))
    (pogo-log "test message %s" "arg")
    (should (get-buffer "*pogo-mode-log*"))
    (with-current-buffer "*pogo-mode-log*"
      (should (string-match-p "test message arg" (buffer-string))))
    (kill-buffer "*pogo-mode-log*")))

(ert-deftest pogo-test-log-disabled ()
  "Test pogo-log does nothing when debug logging is off."
  (let ((pogo-debug-log nil))
    (when (get-buffer "*pogo-mode-log*")
      (kill-buffer "*pogo-mode-log*"))
    (pogo-log "should not appear")
    (should-not (get-buffer "*pogo-mode-log*"))))

;;; pogo-version

(ert-deftest pogo-test-version-returns-string ()
  "Test pogo-version returns a version string."
  (let ((v (pogo-version)))
    (should (stringp v))
    (should (string-match-p "0\\.0\\.1" v))))

;;; pogo-project-name

(ert-deftest pogo-test-project-name-custom ()
  "Test pogo-project-name uses custom name when set."
  (let ((pogo-project-name "my-custom-name"))
    (should (equal (pogo-project-name) "my-custom-name"))))

(ert-deftest pogo-test-project-name-fallback ()
  "Test pogo-project-name returns dash when no project root."
  (let ((pogo-project-name nil))
    ;; Mock pogo-project-root to return nil
    (cl-letf (((symbol-function 'pogo-project-root) (lambda (&optional _) nil)))
      (should (equal (pogo-project-name) "-")))))

;;; pogo-prepend-project-name

(ert-deftest pogo-test-prepend-project-name ()
  "Test pogo-prepend-project-name prepends [project] prefix."
  (let ((pogo-project-name "myproj"))
    (should (equal (pogo-prepend-project-name "Find file:")
                   "[myproj] Find file:"))))

;;; pogo-parse-visit-call (json-read / alist parser)

(ert-deftest pogo-test-parse-visit-call-nil ()
  "Test pogo-parse-visit-call with nil response."
  (let ((pogo-json-parser 'json-read))
    (should (equal (pogo-parse-visit-call nil) nil))))

(ert-deftest pogo-test-parse-visit-call-alist ()
  "Test pogo-parse-visit-call with alist response."
  (let ((pogo-json-parser 'json-read)
        (resp '((project . ((path . "/home/user/proj"))))))
    (should (equal (pogo-parse-visit-call resp) "/home/user/proj"))))

(ert-deftest pogo-test-parse-visit-call-hash ()
  "Test pogo-parse-visit-call with hash-table response."
  (let ((pogo-json-parser 'json-parse-buffer)
        (project (make-hash-table :test 'equal))
        (resp (make-hash-table :test 'equal)))
    (puthash "path" "/home/user/proj" project)
    (puthash "project" project resp)
    (should (equal (pogo-parse-visit-call resp) "/home/user/proj"))))

;;; pogo-parse-result

(ert-deftest pogo-test-parse-result-alist ()
  "Test pogo-parse-result with alist response (json-read parser)."
  (let ((pogo-json-parser 'json-read)
        (resp '((index . ((error . nil) (paths . ("/a" "/b"))))
                (results . (1 2 3)))))
    (let ((result (pogo-parse-result resp)))
      (should (equal (cdr (assoc 'paths result)) '("/a" "/b")))
      (should (equal (cdr (assoc 'results result)) '(1 2 3))))))

(ert-deftest pogo-test-parse-result-alist-error ()
  "Test pogo-parse-result with alist response containing error."
  (let ((pogo-json-parser 'json-read)
        (resp '((index . ((error . "something broke") (paths . nil)))
                (results . nil))))
    (should (equal (pogo-parse-result resp) nil))))

(ert-deftest pogo-test-parse-result-hash ()
  "Test pogo-parse-result with hash-table response (json-parse-buffer parser)."
  (let ((pogo-json-parser 'json-parse-buffer)
        (resp (make-hash-table :test 'equal)))
    (puthash "results" '(1 2 3) resp)
    (puthash "error" nil resp)
    (puthash "paths" '("/a" "/b") resp)
    (let ((result (pogo-parse-result resp)))
      (should (equal (cdr (assoc 'paths result)) '("/a" "/b")))
      (should (equal (cdr (assoc 'results result)) '(1 2 3))))))

;;; pogo-ignored-buffer-p

(ert-deftest pogo-test-ignored-buffer-by-name ()
  "Test pogo-ignored-buffer-p matches buffer name."
  (let ((pogo-globally-ignored-buffers '("\\*scratch\\*")))
    (with-temp-buffer
      (rename-buffer "*scratch*" t)
      (should (pogo-ignored-buffer-p (current-buffer))))))

(ert-deftest pogo-test-ignored-buffer-by-mode ()
  "Test pogo-ignored-buffer-p matches major mode."
  (let ((pogo-globally-ignored-modes '("help-mode")))
    (with-temp-buffer
      (help-mode)
      (should (pogo-ignored-buffer-p (current-buffer))))))

(ert-deftest pogo-test-not-ignored-buffer ()
  "Test pogo-ignored-buffer-p returns nil for non-matching buffer."
  (let ((pogo-globally-ignored-buffers nil)
        (pogo-globally-ignored-modes nil))
    (with-temp-buffer
      (should-not (pogo-ignored-buffer-p (current-buffer))))))

;;; pogo-command-map keybindings

(ert-deftest pogo-test-command-map-has-search ()
  "Test pogo-command-map has search binding on 'g'."
  (should (eq (lookup-key pogo-command-map (kbd "g")) 'pogo-search)))

(ert-deftest pogo-test-command-map-has-find-file ()
  "Test pogo-command-map has find-file binding on 'f'."
  (should (eq (lookup-key pogo-command-map (kbd "f")) 'pogo-find-file)))

(ert-deftest pogo-test-command-map-has-switch-buffer ()
  "Test pogo-command-map has switch-to-buffer binding on 'b'."
  (should (eq (lookup-key pogo-command-map (kbd "b")) 'pogo-switch-to-buffer)))

(ert-deftest pogo-test-command-map-has-dired ()
  "Test pogo-command-map has dired binding on 'D'."
  (should (eq (lookup-key pogo-command-map (kbd "D")) 'pogo-dired)))

(ert-deftest pogo-test-command-map-has-switch-project ()
  "Test pogo-command-map has switch-project binding on 'p'."
  (should (eq (lookup-key pogo-command-map (kbd "p")) 'pogo-switch-project)))

(ert-deftest pogo-test-command-map-has-kill-buffers ()
  "Test pogo-command-map has kill-buffers binding on 'k'."
  (should (eq (lookup-key pogo-command-map (kbd "k")) 'pogo-kill-buffers)))

(ert-deftest pogo-test-command-map-has-recentf ()
  "Test pogo-command-map has recentf binding on 'e'."
  (should (eq (lookup-key pogo-command-map (kbd "e")) 'pogo-recentf)))

(ert-deftest pogo-test-command-map-has-prev-buffer ()
  "Test pogo-command-map has previous-project-buffer binding."
  (should (eq (lookup-key pogo-command-map (kbd "<left>"))
              'pogo-previous-project-buffer)))

(ert-deftest pogo-test-command-map-has-next-buffer ()
  "Test pogo-command-map has next-project-buffer binding."
  (should (eq (lookup-key pogo-command-map (kbd "<right>"))
              'pogo-next-project-buffer)))

;;; Completion system selection

(ert-deftest pogo-test-completion-system-default ()
  "Test that default completion system uses completing-read."
  (let ((pogo-completion-system 'default)
        (pogo-project-name "test"))
    (cl-letf (((symbol-function 'completing-read)
               (lambda (_prompt choices &rest _) (car choices))))
      (should (equal (pogo-completing-read "Pick: " '("a" "b" "c")) "a")))))

;;; pogo-ensure-project

(ert-deftest pogo-test-ensure-project-with-dir ()
  "Test pogo-ensure-project returns dir when non-nil."
  (should (equal (pogo-ensure-project "/some/path") "/some/path")))

(ert-deftest pogo-test-ensure-project-nil-no-require ()
  "Test pogo-ensure-project returns default-directory when no requirement."
  (let ((pogo-require-project-root nil)
        (default-directory "/tmp/"))
    (should (equal (pogo-ensure-project nil) "/tmp/"))))

(ert-deftest pogo-test-ensure-project-nil-require ()
  "Test pogo-ensure-project errors when project root required."
  (let ((pogo-require-project-root t)
        (default-directory "/tmp/"))
    (should-error (pogo-ensure-project nil))))

;;; pogo-symbol-at-point

(ert-deftest pogo-test-symbol-at-point-empty ()
  "Test pogo-symbol-at-point returns empty string when no symbol."
  (with-temp-buffer
    (should (equal (pogo-symbol-at-point) ""))))

(ert-deftest pogo-test-symbol-at-point-word ()
  "Test pogo-symbol-at-point returns symbol text."
  (with-temp-buffer
    (insert "hello world")
    (goto-char 3)  ;; inside "hello"
    (should (equal (pogo-symbol-at-point) "hello"))))

;;; pogo-mode-line

(ert-deftest pogo-test-default-mode-line ()
  "Test pogo-default-mode-line format."
  (let ((pogo-mode-line-prefix " Pogo"))
    (cl-letf (((symbol-function 'pogo-project-name) (lambda (&optional _) "myproj")))
      (should (equal (pogo-default-mode-line) " Pogo[myproj]")))))

(ert-deftest pogo-test-default-mode-line-no-project ()
  "Test pogo-default-mode-line with no project."
  (let ((pogo-mode-line-prefix " Pogo"))
    (cl-letf (((symbol-function 'pogo-project-name) (lambda (&optional _) nil)))
      (should (equal (pogo-default-mode-line) " Pogo[-]")))))

;;; pogo--move-current-project-to-end

(ert-deftest pogo-test-move-current-project-to-end ()
  "Test moving current project to end of list."
  (cl-letf (((symbol-function 'pogo-project-root) (lambda (&optional _) "/home/user/b/"))
            ((symbol-function 'abbreviate-file-name) (lambda (x) x)))
    (let ((projects '("/home/user/a/" "/home/user/b/" "/home/user/c/")))
      (should (equal (pogo--move-current-project-to-end projects)
                     '("/home/user/a/" "/home/user/c/" "/home/user/b/"))))))

(ert-deftest pogo-test-move-current-project-not-in-list ()
  "Test move-to-end when current project is not in list."
  (cl-letf (((symbol-function 'pogo-project-root) (lambda (&optional _) "/home/user/z/"))
            ((symbol-function 'abbreviate-file-name) (lambda (x) x)))
    (let ((projects '("/home/user/a/" "/home/user/b/")))
      (should (equal (pogo--move-current-project-to-end projects)
                     '("/home/user/a/" "/home/user/b/"))))))

;;; pogo--remove-current-project

(ert-deftest pogo-test-remove-current-project ()
  "Test removing current project from list."
  (cl-letf (((symbol-function 'pogo-project-root) (lambda (&optional _) "/home/user/b/"))
            ((symbol-function 'abbreviate-file-name) (lambda (x) x)))
    (let ((projects '("/home/user/a/" "/home/user/b/" "/home/user/c/")))
      (should (equal (pogo--remove-current-project projects)
                     '("/home/user/a/" "/home/user/c/"))))))

;;; pogo-print-and-return

(ert-deftest pogo-test-print-and-return ()
  "Test pogo-print-and-return returns its value."
  (let ((pogo-debug-log nil))  ;; suppress logging
    (should (equal (pogo-print-and-return "msg" 42) 42))
    (should (equal (pogo-print-and-return "msg" "hello") "hello"))
    (should (equal (pogo-print-and-return "msg" nil) nil))))

;;; Daemon start path: connect, do not spawn; and detach what you do spawn
;;
;; See the block comment at the top of this file for what these do and do not
;; touch.

(defun pogo-test--wait-until (predicate &optional seconds)
  "Wait for PREDICATE to return non-nil, up to SECONDS (default 3).
Return the value PREDICATE produced, or nil on timeout.  Polling beats a
fixed `sit-for': a spawn and a signal are both asynchronous, and a sleep long
enough to be reliable on a loaded machine is dead time on an idle one."
  (let ((deadline (+ (float-time) (or seconds 3)))
        result)
    (while (and (not (setq result (funcall predicate)))
                (< (float-time) deadline))
      (sit-for 0.05))
    result))

(defun pogo-test--os-pids-matching (marker)
  "Return pids from the OS process table whose argv contains MARKER.

Reads `list-system-processes', which is the machine's own process table — not
`pogo-process', not `pogo-server-started', and not `process-list'.  That is
deliberate: the code under test used to set every one of those variables
correctly while still spawning a daemon, and a spawn that Emacs has stopped
tracking (which is what a detached spawn is) still appears here.

MARKER is a path inside a per-test temporary directory, so the real pogod
running on this machine can never match it."
  (let (hits)
    (dolist (pid (list-system-processes))
      (let ((args (cdr (assq 'args (process-attributes pid)))))
        (when (and args (string-search marker args))
          (push pid hits))))
    hits))

(defun pogo-test--emacs-pogod-processes ()
  "Return Emacs subprocesses whose argv mentions pogod."
  (seq-filter (lambda (proc)
                (seq-some (lambda (arg) (string-match-p "pogod" arg))
                          (or (process-command proc) '())))
              (process-list)))

(defun pogo-test--sighup-like-emacs-exit (pid)
  "Send PID the SIGHUP that Emacs sends its subprocesses as it exits.

`kill-emacs' reaches `kill_buffer_processes', which signals the subprocess's
process GROUP rather than the bare pid.  Both forms are sent here so that the
test cannot pass on a technicality about which one Emacs happens to choose."
  (ignore-errors (signal-process (- pid) 'SIGHUP))
  (ignore-errors (signal-process pid 'SIGHUP)))

(defun pogo-test--fake-pogod-script ()
  "Return the body of a stand-in pogod.

It installs no signal handlers.  That is the property being borrowed: the real
pogod installs none either, so SIGHUP's default disposition terminates it."
  (concat "#!/bin/sh\n"
          "# Stand-in pogod for pogo-test.el. No signal handlers, on purpose.\n"
          "echo 'stand-in pogod is up'\n"
          "i=0\n"
          "while [ $i -lt 120 ]; do sleep 1; i=$((i+1)); done\n"))

(defun pogo-test--call-with-fake-pogod (fn)
  "Call FN with the path of a stand-in `pogod' placed first on `exec-path'.

Both `exec-path' and PATH are shadowed, because `pogo-start' resolves pogod
through the former while `nohup' resolves anything left bare through the
latter.  On the way out, every process the body spawned is killed and the
pending health-check timer is cancelled — that timer would otherwise fire in
the middle of a later test and spawn again."
  (let* ((dir (make-temp-file "pogo-test-bin" t))
         (fake (expand-file-name "pogod" dir))
         (exec-path (cons dir exec-path))
         (process-environment
          (cons (concat "PATH=" dir path-separator (or (getenv "PATH") ""))
                process-environment)))
    (with-temp-file fake (insert (pogo-test--fake-pogod-script)))
    (set-file-modes fake #o755)
    (unwind-protect
        (funcall fn fake)
      (cancel-function-timers 'pogo-health-check)
      (dolist (proc (pogo-test--emacs-pogod-processes))
        (ignore-errors (delete-process proc)))
      (dolist (pid (pogo-test--os-pids-matching fake))
        (ignore-errors (signal-process pid 'SIGKILL)))
      (ignore-errors (delete-directory dir t)))))

(defun pogo-test--start-fake-daemon ()
  "Start a real listening socket that answers /health, and return it.

Bound to an ephemeral port on the loopback interface: the pogo daemon port is
never involved, so running this suite on a machine with a live pogod cannot
disturb it."
  (make-network-process
   :name "pogo-test-fake-daemon"
   :server t
   :host "127.0.0.1"
   :service t
   :family 'ipv4
   :noquery t
   :filter
   (lambda (conn request)
     (let ((body (if (string-match-p "/health" request)
                     "{\"status\":\"ok\"}"
                   "{}")))
       (ignore-errors
         (process-send-string
          conn (concat "HTTP/1.1 200 OK\r\n"
                       "Content-Type: application/json\r\n"
                       (format "Content-Length: %d\r\n" (string-bytes body))
                       "Connection: close\r\n\r\n"
                       body))
         (process-send-eof conn))))))

(defun pogo-test--call-with-fake-daemon (fn)
  "Call FN with `pogo-server-url' pointed at a live stand-in daemon."
  (let ((server (pogo-test--start-fake-daemon)))
    (unwind-protect
        (let ((pogo-server-url
               (format "http://127.0.0.1:%s" (process-contact server :service))))
          (funcall fn))
      (ignore-errors (delete-process server)))))

(ert-deftest pogo-test-enabling-pogo-mode-spawns-nothing-when-a-daemon-answers ()
  "Enabling `pogo-mode' against a reachable pogod must spawn no second one.

This is the defect.  pogo.el spawned unconditionally at mode enable, so every
Emacs start put a rival pogod beside the daemon launchd already owns; between
2026-08-04 and 2026-08-07 that cost 24879 failed launchd spawns on this
machine.

The assertion is on the OS process table.  Asserting on `pogo-server-started'
or `pogo-process' would have passed against the broken code, which set both
of them correctly while spawning.

`pogo-mode' is enabled for real; the whole decision path runs unstubbed.  The
one stub is `pogo-update-mode-line', which is mode-line rendering and reaches
the daemon over a different route (/file, via `pogo-visit') that has nothing
to do with whether a daemon gets spawned."
  (pogo-test--call-with-fake-pogod
   (lambda (fake)
     (pogo-test--call-with-fake-daemon
      (lambda ()
        (let ((pogo-auto-discover nil)
              (pogo-debug-log nil)
              (pogo-server-started nil)
              (pogo-failure-count 0)
              (pogo-process nil))
          (unwind-protect
              (cl-letf (((symbol-function 'pogo-update-mode-line) #'ignore))
                (pogo-mode 1)
                ;; `start-process' returns only once the child exists, so a
                ;; spawn is already visible here; the wait only gives a
                ;; wrongly-spawned pogod room to finish exec and show its argv.
                (pogo-test--wait-until
                 (lambda () (pogo-test--os-pids-matching fake)) 1)
                (should (equal '() (pogo-test--os-pids-matching fake)))
                (should (equal '() (pogo-test--emacs-pogod-processes)))
                ;; Supporting, secondary: proves the absence above is the probe
                ;; succeeding and not the spawn being impossible for some
                ;; unrelated reason, such as the failure count being spent.
                (should pogo-server-started))
            (pogo-mode -1))))))))

(ert-deftest pogo-test-try-start-does-spawn-when-nothing-answers ()
  "With nothing listening, `pogo-try-start' must still spawn a pogod.

Control for the test above.  Without it, that test would pass just as
contentedly if a spawn were impossible — no pogod on `exec-path', a spent
failure count — rather than suppressed, and would then keep passing after the
probe was removed again."
  (pogo-test--call-with-fake-pogod
   (lambda (fake)
     ;; Port 1 is privileged and unbound: the probe fails at connect, at once.
     (let ((pogo-server-url "http://127.0.0.1:1")
           (pogo-debug-log nil)
           (pogo-server-started nil)
           (pogo-failure-count 0)
           (pogo-process nil))
       (pogo-try-start)
       (should (pogo-test--wait-until
                (lambda () (pogo-test--os-pids-matching fake))))))))

(ert-deftest pogo-test-try-start-refuses-once-the-failure-count-is-spent ()
  "A spent failure count still suppresses the spawn, probe or no probe."
  (pogo-test--call-with-fake-pogod
   (lambda (fake)
     (let ((pogo-server-url "http://127.0.0.1:1")
           (pogo-debug-log nil)
           (pogo-server-started nil)
           (pogo-failure-count pogo-max-failure-count)
           (pogo-process nil))
       (pogo-try-start)
       (pogo-test--wait-until (lambda () (pogo-test--os-pids-matching fake)) 1)
       (should (equal '() (pogo-test--os-pids-matching fake)))))))

(ert-deftest pogo-test-try-start-still-connects-with-no-pogod-to-spawn ()
  "With no pogod on `exec-path', a reachable daemon is still adopted.

`pogo-try-start' used to signal before it ever probed: it built
POGO_PLUGIN_PATH from `(executable-find \"pogod\")' unconditionally, so a
missing binary aborted the function — including on the path where there was a
healthy daemon sitting right there to connect to.  docs/emacs.md offers a
pogod-free `exec-path' as the way to opt out of the fallback spawn, so this
has to hold."
  ;; /bin and /usr/bin carry curl (which `request' shells out to) but not
  ;; pogod, which installs into GOBIN. Skip rather than lie if that is untrue.
  (let ((exec-path '("/bin" "/usr/bin")))
    (skip-unless (and (executable-find "curl")
                      (not (executable-find "pogod"))))
    (pogo-test--call-with-fake-daemon
     (lambda ()
       (let ((pogo-debug-log nil)
             (pogo-server-started nil)
             (pogo-failure-count 0)
             (pogo-process nil))
         (pogo-try-start)
         (should pogo-server-started)
         (should-not pogo-process))))))

(ert-deftest pogo-test-a-spawned-pogod-survives-the-sighup-emacs-sends-on-exit ()
  "A pogod pogo.el spawns must outlive this Emacs.

On 2026-08-07 an Emacs exit took its child pogod with it and the agent fleet
went down: pogod's death force-closes the PTY masters it owns, hanging up the
controlling terminal of every agent it started (mg-6b66, gh #22).  Emacs
SIGHUPs its subprocesses as it exits, so surviving that signal is the whole
requirement."
  (pogo-test--call-with-fake-pogod
   (lambda (fake)
     (let* ((pogo-debug-log nil)
            (proc (pogo-start)))
       (should proc)
       (should (pogo-test--wait-until
                (lambda () (pogo-test--os-pids-matching fake))))
       (pogo-test--sighup-like-emacs-exit (process-id proc))
       ;; Give the signal at least as long to land as the control test needs
       ;; to observe a death, so this cannot pass by being checked too early.
       (sit-for 1)
       (should (pogo-test--os-pids-matching fake))))))

(ert-deftest pogo-test-the-spawn-uses-a-pipe-and-leaves-no-nohup-out ()
  "The spawn must not run over a pty, and must not litter the user's directory.

`process-connection-type' defaults to a pty, and a pty is Emacs's own: it
becomes pogod's controlling terminal, so Emacs closing the master on exit
hangs up that session — the mg-6b66 cascade arriving by a route `nohup' does
not cover.  It is also visible from outside, which is what this test uses:
`nohup' checks isatty(stdout), and on a pty it creates a `nohup.out' in
`default-directory'.  A stray file dropped into whichever project the user was
visiting is the cheap, checkable proxy for the pty being gone.

The buffer assertion is here because the obvious way to silence `nohup.out' —
handing the spawn a redirect or no output at all — would take pogod's startup
output with it, and that output is the only account of a daemon that failed to
bind."
  (pogo-test--call-with-fake-pogod
   (lambda (fake)
     (let* ((default-directory (make-temp-file "pogo-test-cwd" t))
            (pogo-debug-log nil))
       (unwind-protect
           (progn
             (ignore-errors (kill-buffer "*pogo-server*"))
             (should (pogo-start))
             (should (pogo-test--wait-until
                      (lambda () (pogo-test--os-pids-matching fake))))
             (should-not (file-exists-p (expand-file-name "nohup.out")))
             (should (pogo-test--wait-until
                      (lambda ()
                        (with-current-buffer "*pogo-server*"
                          (string-match-p "stand-in pogod is up"
                                          (buffer-string)))))))
         (ignore-errors (delete-directory default-directory t)))))))

(ert-deftest pogo-test-an-undetached-spawn-dies-of-that-same-sighup ()
  "The same stand-in pogod, spawned bare, dies of the signal the other survives.

Control for the test above.  Without it, that test would pass if the stand-in
happened to ignore SIGHUP by itself — and would then say nothing at all about
the real pogod, which does not."
  (pogo-test--call-with-fake-pogod
   (lambda (fake)
     (let ((proc (start-process "pogod-undetached" nil fake)))
       (should (pogo-test--wait-until
                (lambda () (pogo-test--os-pids-matching fake))))
       (pogo-test--sighup-like-emacs-exit (process-id proc))
       (should (pogo-test--wait-until
                (lambda () (null (pogo-test--os-pids-matching fake)))))))))

(ert-deftest pogo-test-detached-argv-goes-through-nohup ()
  "`pogo--detached-argv' prefixes nohup, which is what ignores the SIGHUP."
  (let ((pogo--is-windows nil))
    (let ((argv (pogo--detached-argv "/opt/bin/pogod")))
      (should (equal (file-name-nondirectory (car argv)) "nohup"))
      (should (equal (cadr argv) "/opt/bin/pogod")))))

(ert-deftest pogo-test-detached-argv-is-bare-on-windows ()
  "Windows has neither nohup nor SIGHUP, so the argv is left alone there."
  (let ((pogo--is-windows t))
    (should (equal (pogo--detached-argv "C:/pogo/pogod.exe")
                   '("C:/pogo/pogod.exe")))))

(ert-deftest pogo-test-daemon-reachable-p-reads-the-socket ()
  "`pogo--daemon-reachable-p' answers from the wire, both ways."
  (pogo-test--call-with-fake-daemon
   (lambda () (should (pogo--daemon-reachable-p))))
  (let ((pogo-server-url "http://127.0.0.1:1"))
    (should-not (pogo--daemon-reachable-p))))

(provide 'pogo-test)

;;; pogo-test.el ends here
