---
id: mg-EXAMPLE
type: task
created: 2026-04-25T00:00:00Z
creator: daniel
depends: []
tags: [research]
priority: medium
---

# Investigate streaming-JSON parsers for the ingest pipeline

We need to pick a library for parsing very large JSON arrays (~5–20 GB) from
S3 in the ingest pipeline. Memory budget is 512 MB per worker. The current
hack is to read the whole file into memory, which works for the test data
but blows up on real production payloads.

Questions to answer:

1. What are the leading streaming-JSON libraries in Go? List 3–5 with
   stars / maintenance status.
2. Which of them can parse a top-level JSON array element-by-element
   without loading the whole thing? (Some only stream tokens; some only
   stream values.)
3. For the top 2 candidates, what does benchmark output look like for
   1 GB inputs? Cite any published numbers.
4. Are there gotchas around malformed input — does the library bail on the
   first error, or can it skip and continue?
5. Recommend one. Justify the choice in 2–3 sentences.

Constraints:
- Go 1.22+.
- Pure-Go preferred (the prod runtime is Alpine; cgo is painful).
- License: anything OSI-approved; avoid GPL.

This is the kind of item this workspace is designed for: a research
question, not a code change. The polecat that picks it up should produce a
markdown note at `$NOTES_DIR/research-notes-mg-EXAMPLE.md`, not a PR.
