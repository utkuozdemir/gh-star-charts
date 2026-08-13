# gh-star-charts

Self-hosted star history charts for GitHub READMEs.

GitHub restricted the stargazers API in June 2026, and the hosted chart services stopped working. Because of this, the star history images embedded in many READMEs went blank. This tool replaces them with charts that live in a repository you own and update themselves daily, with no external service left in the serving path.

The default chart look reproduces the classic hand-drawn style of [star-history.com](https://star-history.com), which drew these charts for everyone until the API change. The design credit belongs to them.

This repository's own chart, generated and updated by the tool itself:

<!-- markdownlint-disable no-inline-html -->
<a href="https://github.com/utkuozdemir/gh-star-charts/stargazers">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/utkuozdemir/star-charts/main/charts/utkuozdemir/gh-star-charts/dark.svg" />
    <img alt="Star history of utkuozdemir/gh-star-charts" src="https://raw.githubusercontent.com/utkuozdemir/star-charts/main/charts/utkuozdemir/gh-star-charts/light.svg" />
  </picture>
</a>
<!-- markdownlint-enable no-inline-html -->

## How it works

Reading star timestamps requires write access on the repo since the API change. Therefore the full history is fetched once, locally, using the `gh` login you already have. The result goes into a small public "instance" repository under your account, together with the rendered SVGs and a workflow that keeps them current.

After that first backfill, the current star count is public data. The daily workflow reads it without any credentials, appends a data point, re-renders the images, and pushes to its own repository using the ephemeral `GITHUB_TOKEN` with `contents: write` and nothing else. No new credential is created or stored anywhere, and nothing ever holds write access to the repos being charted. Setup uses the `gh` auth you already have, in memory only.

The workflow the tool writes into your instance repo is self-contained and pins its binary with a SHA-256 checksum inlined into the file. This way, a moved tag or a swapped release asset cannot change what runs in your repository. Upgrades reach the workflow only when you run `gh extension upgrade star-charts && gh star-charts init` yourself.

If updates ever stop, you keep a chart that shows its last update date instead of a broken image.

## Install

```sh
gh extension install utkuozdemir/gh-star-charts
```

## Use

```sh
gh star-charts init utkuozdemir/nvidia_gpu_exporter utkuozdemir/pv-migrate
```

This creates `<your-login>/star-charts`, backfills each repo, and prints a copy-paste embed snippet per chart, e.g.:

```html
<a href="https://github.com/OWNER/REPO/stargazers">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/YOU/star-charts/main/charts/owner/repo/dark.svg" />
    <img alt="Star history of OWNER/REPO" src="https://raw.githubusercontent.com/YOU/star-charts/main/charts/owner/repo/light.svg" />
  </picture>
</a>
```

The other commands:

- `add owner/repo` tracks more repos. Re-running it on a tracked repo is a repair: it re-renders, picks up a rename, and updates the workflow pin.
- `remove owner/repo` pauses a chart. Its files and URLs keep serving the last state, so nothing embedded anywhere breaks. `--purge` deletes the files instead, and the URLs start returning 404.
- `reset owner/repo` rebuilds a chart's history from scratch. This is destructive, because the daily observations collected so far (including unstar dips) cannot be fetched again, so it asks for confirmation.
- `update` is the entry point the workflow runs. You normally never call it yourself.

## Styling

The default is the hand-drawn look with the classic coral line, in light and dark variants. The colors are contrast-checked against GitHub's README backgrounds.

Styling is per chart, set through flags on `add`. Re-running `add` on a tracked repo just applies the changes:

```sh
gh star-charts add owner/repo --line-color '#2a78d6'
gh star-charts add owner/repo --look clean
```

`--line-color` applies to both variants unless `--line-color-dark` is also given, and the value `none` clears an override. `--look clean` switches a chart to a plain line-and-area style without the hand-drawn wobble, and `--look default` goes back to the tool's default.

One limitation of the hand-drawn look: the comic lettering uses fonts from the viewer's system (e.g., Comic Sans on Windows, Chalkboard on macOS), because an SVG embedded in a README cannot load web fonts. On a machine without any comic font the labels fall back to a plain face, while the hand-drawn line work stays.

## Limitations

- History from before the setup is reconstructed from the stars that still exist, so anyone who unstarred earlier is invisible. From the setup on, the chart records one observed total per day.
- The API serves at most the first 40k stars of the history. A repo beyond that starts its chart at setup time, with a caption saying so.
- The backfill needs write access on the charted repo. This is GitHub's restriction, not ours, and it means you can chart your own repos and the ones you maintain.
- Private repos are rejected: the chart would publish their star counts, and the updater could not read them anyway.
- GitHub disables the schedule of a public repo's workflow after 60 days without repository activity. The daily commits probably prevent this, but if it ever happens, recovery is one click on the email GitHub sends, and any `init` or `add` run re-enables the workflow too.
- The instance repo's default branch name is part of every embed URL, so do not rename it.
- GitHub.com only, no GHES.

## License

MIT.
