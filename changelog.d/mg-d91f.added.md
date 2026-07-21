- **A ratchet on the `--body="..."` idiom in shipped prompt templates, and the quoted-heredoc
  form promoted to the leading example (mg-d91f).** mg-7850 and mg-8380 gave both `mg` and
  `pogo agent spawn-polecat` a `--body-file` that bypasses the shell, and the number did not
  move: an agent copies its idioms from its own prompt, not from `--help`. A walk of
  `internal/agent/prompts/` found 40 example lines teaching `--body="..."` and **zero**
  teaching any safe form — and more new unsafe examples were added in the last 30 days than
  the entire standing count, so a one-time sweep would be a snapshot fix on a target our own
  authoring moves. `internal/agent/bodyratchet_test.go` now freezes the existing sites in a
  path-keyed inventory that may only shrink, and fails any NEW one with a message naming the
  remedy. The predicate is anchored to **example lines** — fenced or indented command blocks,
  comments and prose exempt — so a template that documents "never use `--body=`" is not
  punished by the check that wants exactly that. An unquoted `<<EOF` on a `--body-file`
  example is a violation with no grandfathering: it expands identically to `--body="..."` and
  reintroduces the bug in a diff that looks like the fix. `pogo agent spawn-polecat --help`
  now leads with the safe one-liner:

      pogo agent spawn-polecat cat-1234 --id mg-1234 --body-file - <<'EOF'
      body text with `backticks` and $VARS, all literal
      EOF

  `--body` is demoted, **not** deprecated or gated. Gating it was refuted with a positive
  control now standing in `cmd/pogo/bodymetachar_test.go`: a metacharacter guard reading argv
  sits downstream of the corruption, so it scored **0/2 detection on real corruptions and 1/1
  false positive on correct usage**. The dangerous case is an unset `$VAR`, which deletes the
  object of a safety constraint and leaves prose that still reads as intentional.
