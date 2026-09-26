- **`pogo check-turns` closed with "Every present agent has completed a turn
  within the window" over a fleet missing members, and nothing in its output
  said so (mg-d88e).** Its population is pogod's registry, and a stopped agent
  is not a `silent` row there — it is no row at all. On 2026-09-06 it examined
  six agents and printed that line while doctor and representative were down.
  Every run now also reads the configured roster (the same read as `pogo agent
  list`'s footer) and prints a `NOT EXAMINED` line naming the absent and parked
  configured agents; a clean run with an absence adds "This is NOT a fleet-wide
  green", and a roster that cannot be read says so instead of going quiet.
  `--json` gains a `roster` object beside the unchanged report fields. The exit
  status is unchanged — judging an absence is absent-watch's job — and the
  registry array the other eight consumers iterate is untouched. The mayor
  prompt now covers the population = N−1 case beside the empty-population one:
  silence from a registry consumer carries no information about a missing agent.
