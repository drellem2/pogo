- **Crew prompts can declare environment variables (drellem2/pogo#263).** A
  crew prompt's frontmatter takes `env = ["KEY=VALUE", ...]`, on one line or
  several. pogod applies it beneath its own identity variables, so a prompt
  cannot override `POGO_AGENT_NAME` or `POGO_ROLE`. The value captured at start
  is re-applied on every crash restart. A malformed `env` line is skipped with
  a warning and the rest of the frontmatter still applies. Variable names, not
  values, are logged at start.
