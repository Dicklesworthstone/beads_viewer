## Permanent CI/automation policy: DSR only

GitHub Actions must NEVER be used for ANY purpose in this project. Do not create, enable, dispatch, rerun, or rely on GitHub Actions workflows, whether they use GitHub-hosted runners or self-hosted runners registered with Actions. This is permanent policy, not a billing, quota, or outage workaround.

Use [DSR (Doodlestein Self-Releaser)](https://github.com/Dicklesworthstone/doodlestein_self_releaser) directly for CI, checks, builds, tests, releases, deployment automation, scheduled jobs, and other automation. DSR is the execution path, not a fallback after trying Actions. Existing workflow YAML may remain as input for local DSR/`act` execution; that does not authorize execution through GitHub Actions. Do not delete those definitions merely to enforce this policy. Keep all existing quality gates and evidence requirements when routing work through DSR.

Chroma is a syntax highlighting library, tool and web playground for Go. It is based on Pygments and includes importers for it, so most of the same concepts from Pygments apply to Chroma.

This project is written in Go, uses Hermit to manage tooling, and Just for helper commands. Helper scripts are in ./scripts.

Language definitions are XML files defined in ./lexers/embedded/*.xml.

Styles/themes are defined in ./styles/*.xml.

The CLI can be run with `chroma`.

The web playground can be run with `chromad --csrf-key=moo`. It blocks, so should generally be run in the background. It also does not hot reload, so has to be manually restarted. The playground has two modes - for local development it uses the server itself to render, while for production running `just chromad` will compile ./cmd/libchromawasm into a WASM module that is bundled into `chromad`.
