- **The nightly deploy keeps this box's enth current (mg-1be6c).** After the
  fleet-mg step, every attempting fire finds enth's newest CI-green `main`
  commit, using GitHub check runs along the first-parent line. It runs that
  commit's own `scripts/install.sh` (drellem2/enth#53) into a staging prefix,
  and swaps the whole set into `~/.local/bin` only when every binary reports
  one version. A red CI, a failing `install.sh` or a failed check after the
  swap leaves the previous set in place: `.enth-prev` is restored and checked
  byte for byte. Each fire writes one line,
  `enth: installed version=… sha=… green=… head=… prev_version=… prev_sha=… result=… rc=…`,
  for deploy-verify to read. It never changes the run's exit status.
  `pogo-deploy.sh --enth-only` runs the step alone.
