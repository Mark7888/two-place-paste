<!-- TODO: Write the full CLAUDE.md (build/test commands, repository layout,
     conventions, review rules) in a follow-up PR. Only the documentation rule
     below is in place so far. -->

# CLAUDE.md

## Keep the documentation site up to date

The user and operator documentation lives in `docs/site/` (Starlight, published
to https://mark7888.github.io/two-place-paste/). **Every change that adds a
feature or changes behaviour must update it in the same PR.** That includes:

- new or renamed settings, buttons, tabs, tray items or other UI text;
- install, update or uninstall steps on any platform;
- server configuration variables, defaults, endpoints, CLI commands or the
  Docker / compose setup;
- anything a user or operator would notice.

Pages are in `docs/site/src/content/docs/`; the sidebar is in
`docs/site/astro.config.mjs`. When the UI changes visibly, retake the affected
screenshots in `docs/site/src/assets/screenshots/`. Check the site still builds
(this also validates every internal link):

```sh
cd docs/site && npm ci && npm run build
```

Design documents for contributors (spec, roadmap, conventions, feature plans)
are separate and live in `docs/internal/`.
