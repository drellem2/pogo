# Pogo Vision
Pogo is the center of the next-generation development experience. It takes the approach of "operating system" rather than "IDE", providing a flexible, lightweight interface to the entire agent-first development experience.

# Principles
- Pogo should follow UNIX principles. The first big ramification is that agents should be simple unix processes.
- Devs do not have to "import" anything - it should be picked up automatically.
- Devs should not have to wait for indexing etc. to occur - it should be done in the background.
- Latency should be minimized. Quantitative differences here lead to qualitative differences in outcomes.
- Pogo can be easily used by both humans and agents.


# pogod

The pogo daemon is like `projectile.el`: it tracks projects and files. It also does zoekt indexing for code search. This is the first step to 

# Next Steps

The next big step is to introduce agent orchestration built in github.com/drellem2/macguffin. macguffin is an alternative to beads, providing an simple interface to agent task-tracking.

I want to grow pogo into an "OS" for agent-orchestration-centered dev workflows. Specifically, I want to recreate a Gas-Town sort of experience with a few differences:
- Conceptual simplicity / embracing the bitter lesson. I don't want too many hacks to work around today's limitations if they'll be obviated in 6 months.
- Integration with UNIX. Agents should be UNIX processes that behave like any other process and can be managed via straightforward tools. Extra functionality on top should be convention & ergonomics, for instance we may have process name structures for long-running vs ephemeral agents, and there may be command-line utilities to query, nudge, check status etc.
- It will use pogo & follow the existing pogo principles. Namely, instead of imported "rig"s, repos should be discovered via pogo in the background. Another way to think about this is "operating system vs IDE", it's a flexible set of tools that can interoperate with anything.

