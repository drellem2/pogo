- **`pogo gc`'s dry run now previews the session-temp removals `--apply` makes
  for a subdirectory session of a tree reclaimed in the same pass
  (drellem2/pogo#203, mg-fd3e5).** A Claude session started in
  `<polecat>/<sub>` has a temp dir spelled like a polecat named
  `<polecat>-<sub>`, which the orphan-temp phase keeps while `<polecat>`'s
  directory exists. `--apply` deletes that directory first and so removed the
  temp dir, while a dry run still saw the directory and reported it kept. A dry
  run now counts a tree it would reclaim as gone. `--apply` is unchanged.
