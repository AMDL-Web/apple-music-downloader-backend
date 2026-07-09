# Contributing

## Developer Certificate of Origin (DCO)

All commits must be signed off under the [Developer Certificate of Origin](https://developercertificate.org/). By signing off, you certify that you wrote the change or otherwise have the right to submit it under this project's license.

Sign off every commit with the `-s` flag:

```sh
git commit -s -m "your commit message"
```

This appends a `Signed-off-by` trailer using your configured `git config user.name` / `user.email`:

```
Signed-off-by: Your Name <you@example.com>
```

If you forgot to sign off a commit, amend it:

```sh
git commit --amend -s
```

For multiple commits in a branch:

```sh
git rebase --signoff origin/main
```

Pull requests are checked by the `DCO` CI workflow ([.github/workflows/dco.yml](.github/workflows/dco.yml)) and will fail if any commit is missing a valid `Signed-off-by` trailer.
