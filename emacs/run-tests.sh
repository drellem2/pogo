#!/usr/bin/env bash
# Run pogo.el ERT tests in batch mode.
# Installs required packages (request, pcache) into a temporary directory.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ELPA_DIR="${SCRIPT_DIR}/.test-elpa"

echo "Installing Emacs package dependencies..."
emacs --batch --eval "(progn
  (require 'package)
  (setq package-user-dir \"${ELPA_DIR}\")
  (add-to-list 'package-archives '(\"melpa\" . \"https://melpa.org/packages/\") t)
  (package-initialize)
  (package-refresh-contents)
  (unless (package-installed-p 'request) (package-install 'request))
  (unless (package-installed-p 'pcache) (package-install 'pcache))
  (message \"Dependencies installed.\"))" 2>&1

echo "Running tests..."
emacs --batch --eval "(progn
  (require 'package)
  (setq package-user-dir \"${ELPA_DIR}\")
  (package-initialize))" \
  -L "${SCRIPT_DIR}" \
  -l "${SCRIPT_DIR}/pogo-test.el" \
  -f ert-run-tests-batch-and-exit 2>&1

exit_code=$?
echo "Tests finished with exit code ${exit_code}"
exit ${exit_code}
