# GoRouter

GoRouter is a lightweight local AI routing daemon with a CLI/TUI client.

The project is intended to provide the useful core of 9router:

- multiple provider nodes and credentials;
- automatic model discovery from provider `/models` endpoints;
- logical model names and nested fallback combos;
- explicit public-model publishing so internal combos can stay hidden;
- round-robin, priority and fallback routing;
- OpenAI-compatible API endpoints with provider adapters;
- SQLite-backed configuration and runtime state;
- a local daemon controlled by CLI/TUI instead of a web dashboard.

Architecture notes live in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
