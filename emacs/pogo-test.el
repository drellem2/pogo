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
;; ERT tests for pogo.el utility functions.
;; These tests cover pure functions that do not require a running pogo server.

;;; Code:

(require 'ert)
(require 'cl-lib)

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

(provide 'pogo-test)

;;; pogo-test.el ends here
