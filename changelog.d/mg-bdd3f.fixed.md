- **pogo-deploy:** the post-bounce "mail-check schedules LOST" mail now says
  why it may name drained polecats. When the agent registry was unreadable
  after the bounce, the UNFILTERED marker reaches the mail body instead of
  being stripped by its caller. When the agent list was unreadable BEFORE the
  bounce (so no owner's type is known), the mail says "OWNER TYPES UNREADABLE",
  and a gone agent of unknown kind gets a check-first remedy instead of the
  crew "start it" one. The post-bounce check is now its own function and is
  run end to end in the tests against a stubbed `pogo`. Refs drellem2/pogo#122
