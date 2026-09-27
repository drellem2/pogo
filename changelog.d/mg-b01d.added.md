- **`com.pogo.mgbackup`: hourly commit and push of `~/.macguffin` to a bare
  repo outside it, `~/backups/macguffin.git`, plus `scripts/mg-store-restore.sh`
  (mg-b01d).** The `.git` from `mg init --git` lives inside the store and was
  lost with it in the 2026-09-27 `rm -rf`. `mg` never commits on its own (only
  `mg snapshot` commits), so nothing was being snapshotted. The job only
  appends commits: a refused push alerts `mayor` rather than being forced. The
  restore script recreates the empty maildir and work dirs that git cannot
  store. Each run also pushes to the private GitHub repo
  `drellem2/macguffin-store`, and refuses to push unless `gh` reports it
  `PRIVATE` on that run, so losing the whole disk is covered too.
