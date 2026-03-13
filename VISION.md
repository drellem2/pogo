# Pogo Vision

Pogo is the center of the next-generation development experience. It inverts the paradigm: rather than integrating tools into one bloated IDE, it provides a flexible, lightweight interface to various tools.

The foundation of pogo is its record of all repositories on the machine. It collects, updates, and indexes repositories automatically in the background and via tool integrations.

## Principles

- Devs do not have to "import" anything - it should be picked up automatically.
- Devs should not have to wait for indexing etc. to occur - it should be done in the background.
- Latency should be minimized. Quantitative differences here lead to qualitative differences in outcomes.
- Pogo should follow UNIX principles.
- Pogo can be easily used by both humans and agents.

## Example Flows

1. You (or an agent) clone a git repository and cd into it. This updates pogo in the background via the zsh integration.
2. When you "switch projects" in emacs the new repo pops up automatically.
3. When you search in the repository it's already indexed.
4. When you change a file, the daemon picks up the change and updates the index.

## Goals

- Make all local development transparent and accessible to all tools.
- Allow the user to follow any development practices they want. Do not enforce any patterns.
