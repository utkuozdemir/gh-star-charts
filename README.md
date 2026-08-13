# gh-star-charts

Self-hosted GitHub star history charts for your READMEs.

GitHub restricted the stargazers API in mid-2026, which broke the hosted chart services many READMEs embedded. This tool replaces them with charts you own end to end:

- **One command setup.** `gh star-charts init owner/repo` backfills the full star history through your existing `gh` login, renders light and dark SVGs, and publishes them in a small public "instance" repo under your account.
- **No tokens, ever.** Setup uses the `gh` auth you already have. The daily update workflow needs only the public star count and its own repo's ephemeral `GITHUB_TOKEN`. Nothing to create, store, rotate, or leak.
- **Nothing third-party in the serving path.** The images are files in your repo, served by GitHub. If updates ever stop, you keep a stale chart that says so, never a broken image.
- **Your repos stay clean.** All commits land in the instance repo. The repos being charted are never written to.

## Install

```sh
gh extension install utkuozdemir/gh-star-charts
```

## Use

```sh
gh star-charts init utkuozdemir/nvidia_gpu_exporter utkuozdemir/pv-migrate
```

This creates `<your-login>/star-charts`, backfills each repo, and prints a copy-paste embed snippet per chart, like:

```html
<a href="https://github.com/OWNER/REPO/stargazers">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/YOU/star-charts/main/charts/owner/repo/dark.svg" />
    <img alt="Star history of OWNER/REPO" src="https://raw.githubusercontent.com/YOU/star-charts/main/charts/owner/repo/light.svg" />
  </picture>
</a>
```

Other verbs: `add` tracks more repos, `remove` pauses a chart (its URLs keep serving the last state; `--purge` deletes), `reset` destructively rebuilds history, `update` is the workflow's entry point.

## How it works, and the trust story

Reading star history timestamps now requires write access on a repo, so the backfill happens once, locally, as you. Afterwards the current star count is public data: the instance repo's workflow polls it daily, appends to a committed `data.json`, re-renders the SVGs, and pushes to itself with an explicit `contents: write` permission block and nothing else.

The workflow written into your instance repo is self-contained and pins its binary by an inlined SHA-256, so no tag move, asset swap, or upstream change can alter what runs in your repo. Upgrades happen only when you run `gh extension upgrade star-charts && gh star-charts init` again.

Recorded history is treated as irreplaceable: observed daily totals (including unstar dips) are never rewritten by later backfills; only an explicit `reset` may rebuild a curve.

## Limitations

- Pre-setup history is reconstructed from surviving stars; anyone who unstarred before setup is invisible. Post-setup, the curve is daily samples.
- Repos beyond the API's 40k-star history cap start their chart at setup time, honestly captioned.
- Backfill needs write access on the charted repo (GitHub's restriction, not ours), so you can chart your own repos and those you maintain.
- Private repos are not supported: the chart would publish private data and the updater could not read them.
- GitHub auto-disables cron workflows in public repos after 60 days without activity. Normal operation likely prevents that, and recovery is one click on GitHub's email, or any `init`/`add` run.
- GitHub.com only.

## License

MIT.
