# docs

| Folder | What it is | Who it is for |
|---|---|---|
| [`site/`](site/) | The documentation website, built with [Starlight](https://starlight.astro.build) and published to <https://mark7888.github.io/two-place-paste/>. Install guides, usage, self-hosting. | People using or hosting TwoPlacePaste |
| [`internal/`](internal/) | The specification, roadmap, engineering conventions and design plans for individual features. | Contributors |

Anything a user or operator needs belongs on the site. Anything about *why* the
code is the way it is belongs in `internal/`.

To work on the site:

```sh
cd docs/site
npm ci
npm run dev        # http://localhost:4321/two-place-paste/
npm run build      # also checks every link
```
