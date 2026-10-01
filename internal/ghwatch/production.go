package ghwatch

import (
	"os/exec"
	"path/filepath"

	"github.com/drellem2/pogo/internal/carrierdrift"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/ghintake"
	"github.com/drellem2/pogo/internal/ghteardown"
	"github.com/drellem2/pogo/internal/ghtoken"
)

// Production binds Deps to the real store, `gh`, `mg mail` and the credential
// cred (the caller's ghtoken.Ensure result). caches must be the ones the caller
// persists afterwards (ReadCaches / WriteCaches), or the fork savings are lost.
//
// It returns the watched intake repos and where that list came from, for the
// caller's report.
func Production(cfg *config.Config, home string, caches Caches, cred ghtoken.Result) (Deps, []string, string) {
	_, ghPathErr := exec.LookPath("gh")

	tdSrc := ghteardown.MGSource{}

	// The watch list is resolved per run; a run is short-lived, so this is the
	// "picked up at the next restart" pogod had, one fire later.
	repos, repoSrc := ghintake.ResolveRepos(nil, cfg.GHIntake.Repos,
		filepath.Join(home, ghintake.PollerStateDirName))
	inSrc, cdSrc := CarrierSources(cfg, caches)
	// The credential predicate is this run's, re-evaluated on every fire —
	// which is what pogod could not do (mg-4d59).
	credState, credSrc := ghintake.CredentialFor(cred.OK(), string(cred.Source))
	// ...and re-asked per sample on the FAILURE PATH ONLY: Reverify runs nothing
	// when no repo failed, and what it spends on a failed one is a single HTTPS
	// request, not a `gh` subprocess.
	verify := ghintake.VerifierFor(ghtoken.RejectionProbe)

	return Deps{
		GHPathErr:        ghPathErr,
		CredentialOK:     cred.OK(),
		CredentialSource: string(cred.Source),
		Credential:       cred.String(),
		Mail:             client.SendMGMail,
		TeardownSource:   tdSrc.Carriers,
		// RetryingLookup / RetryingSnapshot: this box's network is ~50%
		// intermittent (mg-0ffc), and an un-retried read turns one blip into a
		// pass of non-answers (mg-dd22). The check-* CLIs bind the same wrappers.
		TeardownLookup: ghteardown.RetryingLookup(ghteardown.GHLookup),
		IntakeSource: func() (ghintake.Inventory, error) {
			inv, err := ghintake.Collect(repos, ghintake.GHOpenIssues, inSrc.Carriers, inSrc.Statuses(), credState, credSrc)
			if err != nil {
				return inv, err
			}
			return ghintake.Reverify(inv, verify), nil
		},
		IntakeRepos:      repos,
		IntakeRepoSource: repoSrc,
		DriftSource:      cdSrc.Carriers,
		DriftSnapshot:    carrierdrift.RetryingSnapshot(carrierdrift.GHSnapshot),
		DriftStatuses:    cdSrc.Statuses(),
	}, repos, repoSrc
}

// CarrierSources builds the intake and carrier re-read store scans on caches.
//
// A function of its own because the one property that matters here is
// invisible in a review: each source must scan through the PERSISTED cache, or
// every fire forks `mg show` once per item in the store (drellem2/pogo#179;
// ~4,000 forks per 15-minute pass on the live store). Dropping `Cache:`
// compiles and passes every watcher test. TestCarrierSourcesUseThePersistedCaches
// pins it, and TestProductionScansThroughCarrierSources pins that Production
// calls it.
func CarrierSources(cfg *config.Config, caches Caches) (ghintake.MGSource, carrierdrift.MGSource) {
	return ghintake.MGSource{Cache: caches.Intake},
		carrierdrift.MGSource{IncludeShelved: cfg.CarrierDrift.IncludeShelved, Cache: caches.CarrierDrift}
}
