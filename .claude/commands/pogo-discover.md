---
description: Discover repos related to your current work using pogo
allowed-tools: Bash(lsp:*), Bash(pose:*), Bash(pogo visit:*), Bash(git remote:*), Bash(git rev-parse:*), Bash(pwd:*)
argument-hint: [query]
---

Discover local repositories related to your current work using pogo's multi-repo
discovery tools.

Arguments: $ARGUMENTS

## Workflow

### Step 1: Ensure pogo knows about the current repo

Register the current working directory with pogo so it's indexed:

```bash
pogo visit "$(pwd)"
```

### Step 2: List known projects

Get all repos pogo has discovered:

```bash
lsp --json
```

Report the total count and list repo names/paths.

### Step 3: Search for related code

If the user provided a query argument, search across all repos for it:

```bash
pose --json "$ARGUMENTS"
```

If no query was provided, infer a useful search from context:
- Use the current repo name or directory name as a search term
- Search for imports, references, or dependencies that mention this project

```bash
REPO_NAME=$(basename "$(git rev-parse --show-toplevel 2>/dev/null || pwd)")
pose --json "$REPO_NAME"
```

### Step 4: Report results

Summarize findings in this format:

```
## Repo Discovery Report

**Known repos:** <count> projects indexed by pogo
**Query:** <what was searched>

### Related repos (<count> with matches)

| Repo | Matches | Top files |
|------|---------|-----------|
| <name> | <count> | <file1>, <file2>, ... |

### How these repos relate
<Brief description of how the matched repos connect to the current work>
```

If no matches are found, suggest alternative queries the user could try.
