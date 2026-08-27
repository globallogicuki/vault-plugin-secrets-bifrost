# CLAUDE.md

Project guidance for `vault-plugin-secrets-bifrost` - a HashiCorp Vault secrets engine that manages Bifrost credentials. Currently in the design phase (see `docs/`); no plugin code yet.

## Scope & direction

- **Direction: Vault -> Bifrost.** Vault is the control plane; it provisions, leases, rotates, and revokes Bifrost credentials by calling Bifrost's **management API**. Vault is the client of Bifrost.
- **Do not rebuild Bifrost's native Vault consumption.** Bifrost already ships an enterprise `hashicorp-vault` secret backend that resolves `vault.<path>` references at runtime (Bifrost -> Vault). That is the opposite direction and out of scope. See `docs/01-overview.md`.
- **MVP = dynamic virtual keys** (`creds/<role>` issues and auto-revokes a Bifrost VK); provider API keys are phase 2; static roles/rotation phase 3.
- **Language: Go**, on `github.com/hashicorp/vault/sdk` (`framework.Backend`, `logical`). Path layout mirrors the `database`/`aws` engines. Licence: MPL-2.0.

## Bifrost API facts (ground the design; verify against docs.getbifrost.ai before coding)

- Management APIs authenticate with `Authorization: Bearer <management token>` (`ManagementBearerAuth`). Virtual keys / `x-api-key` are **not** accepted on `/api/*`.
- Virtual keys: `POST /api/governance/virtual-keys` (+ list/get/update/delete). Response returns both `id` and the secret `value` (`sk-bf-...`).
- Provider keys: `POST /api/providers/{provider}/keys` (+ CRUD); `value` is **redacted** in responses (capture at create time).
- **No native rotate endpoint** - rotation = create-new + delete-old, orchestrated by the engine.
- Management tokens appear to be **dashboard-created only**; no confirmed API to create/rotate/delete them. This blocks *fully automatic* root rotation, which degrades to operator-assisted until confirmed (tracked as an open question in `docs/09-roadmap.md`).

## Documentation style

Apply these conventions to the README, all files under `docs/`, and any new Markdown.

- **British English.** Use `-ise`/`-isation` (initialise, standardise), `behaviour`, `centre`, `licence` (noun) / `license` (verb), `defence`. Keep original spelling for proper nouns and literals: "Mozilla Public License 2.0", the `LICENSE` filename, and the HTTP header `Authorization`.
- **Hyphens, not em dashes.** Never use em dashes (`—`) or en dashes (`–`). Use a spaced hyphen (` - `) for parenthetical breaks.
- **Concise.** Prefer short, direct sentences. Cut filler ("in order to" -> "to", "there is/are", "it should be noted that"). Lead with the point.
- **Structure.** Favour tables, bullet lists, and short sections over long prose. Use fenced code blocks for commands, config, and Go snippets.
- **Diagrams: prefer Mermaid.** Use fenced ` ```mermaid ` blocks for flow, component, and sequence diagrams (flowcharts and sequence diagrams render on GitHub/GitLab). Keep plain text only where Mermaid does not fit, e.g. directory/path trees. In sequence diagrams avoid HTML entities like `&lt;`/`&gt;` in participant aliases and messages (they break the parser) - use plain placeholders such as `:role` / `:id`. `<br/>` is fine in flowchart node labels. Validate new/changed diagrams with `mermaid.parse` before committing.
- **Cross-reference** related docs by number, e.g. `[04](./04-bifrost-integration.md)`.
- **Ground claims in source.** Bifrost API facts come from docs.getbifrost.ai; mark unverified assumptions as TODO and pin the Bifrost version before implementation.

### cSpell

`creds`, `mgmt`, `getbifrost`, and British spellings (`behaviour`, `defence`, `licence`) are correct and may be flagged by cSpell as unknown words. Treat these as false positives.

## Commits

Commit types drive release versions: `release-please` reads the commits merged to `main` and proposes the next version from them. `feat` bumps the minor, `fix` and `perf` the patch, `feat!` or a `BREAKING CHANGE:` footer the minor while below 1.0.0; everything else releases nothing. Getting the type wrong therefore ships the wrong version number. See `docs/12-build-and-release.md`.

- Use **[Conventional Commits](https://www.conventionalcommits.org/)**: `type(scope): subject`.
  - **Types:** `feat`, `fix`, `docs`, `refactor`, `test`, `build`, `chore`, `ci`, `perf`, `style`, `revert`.
  - **Scope** is optional and names the affected area, e.g. `feat(creds):`, `docs(api-spec):`, `fix(client):`.
  - **Subject:** imperative mood, lower case, no trailing full stop.
  - **Breaking changes:** add `!` before the colon (`feat(config)!:`) and/or a `BREAKING CHANGE:` footer.
  - Example: `feat(creds): include caller display name in virtual-key names`.
- Do **not** add a `Co-Authored-By: Claude` trailer (or any Claude/AI attribution) to commit messages.
- Keep messages concise: a short imperative subject, followed by a body explaining the why when the change is non-trivial.
