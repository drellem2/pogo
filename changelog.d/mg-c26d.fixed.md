- **`pogo host load` answers again — the endpoint was registered but mounted
  nowhere, and had 404'd for two weeks (mg-c26d).** `GET /hostload` was
  registered on pogod's `orchestrated` sub-mux at `internal/agent/api.go:526`.
  That sub-mux is mounted onto `DefaultServeMux` under four prefixes and only
  four — `/agents/`, `/agents`, `/refinery/`, `/scheduler/` — so the request
  never entered it. It fell through to `http.HandleFunc("/", homePage)` and
  returned `404 page not found`. Discriminated live against the same base URL
  at the running revision: `/agents` 200, `/agents/roster` 200 (a route added
  that day, so the daemon was current), `/version` 200, `/hostload` **404**.
  Introduced by `1dd47ad` on 2026-07-30; found 2026-08-13.

  **`/hostload` was the only agent route outside `/agents`, which is exactly
  why it was the only one broken.** So the fix is the move, not a fifth mount:
  the route is now `/agents/hostload`, and `internal/client/agent.go` asks for
  it there. The alternative — adding `http.Handle("/hostload", …)` to both
  wiring branches — is a smaller diff that leaves the property that produced
  the bug in place for the next route to inherit. Nothing regresses on the
  move, because there is no working client to break: the old path has never
  answered at any revision that contained it.

  **What was lost was the preview, not the enforcement.** `HostLoadGate.
  DispatchLoad()` is an in-process call on the spawn path, not an HTTP round
  trip, so the 503 refusal and the per-repo cap worked throughout. What no
  coordinator could do was *read the number pogod would refuse on* before
  planning a batch — which is precisely the drift the handler's own doc comment
  says it exists to prevent, and the mayor prompt names `pogo host load` twice
  as the thing to consult before filling a slot.

  **The more interesting half is why three green tests did not notice.** All
  three called `reg.handleHostLoad(rr, httptest.NewRequest("GET", "/hostload",
  …))` — the handler directly, bypassing the mux. They were right to pass: the
  handler computes correctly, including `POST -> 405` and the per-repo
  occupancy. The path string in those calls is inert, because nothing routes on
  it. **A request a test constructs itself cannot fail on a mount**, so the
  handler was tested at the near end and named for the far end, and the one
  component that would have caught it — the client, which is what builds the
  path — had no test against a live mux at all.

  Verified by counterfactual rather than asserted. With the route put back at
  `/hostload`, all five existing handler-level tests still pass and all six new
  ones fail, each naming the mount.

  So the regression tests go through the mux:

  - `internal/apimount` now holds the mount — the prefix list and the one
    `Mount` function pogod calls from *both* of its wiring branches, which
    previously open-coded the same four `http.Handle` lines. It is a package
    rather than a helper in `cmd/pogod` because nothing can import `main`, so
    the only way to test a route end-to-end was to re-declare the mount inside
    the test: a copy that can agree with a test and disagree with the daemon.
  - `RegisterHandlers` is driven from a route list that `RoutePatterns()`
    exposes, so `TestEveryAgentRouteIsMounted` enumerates *every* agent route,
    issues a real request through a real mounted mux, and fails if it lands on
    the home-page stand-in. It probes with `OPTIONS`, which no handler accepts,
    so every route refuses at its method guard without spawning, parking or
    measuring anything — the response says "a handler was reached" and nothing
    else, which is the only question a mount test should ask.
  - `internal/client` tests `GetHostLoad` against a mux built from the daemon's
    own two pieces, `RegisterHandlers` plus `apimount.Mount` — not from the path
    the test is about to request, since a test that mounts what it then asks for
    proves only that it can agree with itself.

  **The remedy is an artifact of the same kind as the defect, and the check
  caught it.** `apimount.Covers` is a *claim* about reachability that is not
  itself routing — the same shape as a registration that looks like reach and is
  not — so it is pinned against a real `ServeMux` across every prefix boundary.
  That test failed on first run: `Covers("/refinery")` said unreachable, and a
  real mux says reachable, because registering the subtree pattern `/refinery/`
  makes `ServeMux` answer `/refinery` with a 301 into it. The model was wrong
  and got corrected by the thing it models, which is the entire reason it is
  checked that way.

  Swept while in there: `/hostload` was the only orphan. Every remaining route
  on `orchestrated` — 15 agent, 7 refinery, 4 scheduler — begins with a mounted
  prefix, and `TestEveryAgentRouteIsCoveredByAMountedPrefix` now fails the build
  if an agent route stops doing so.
