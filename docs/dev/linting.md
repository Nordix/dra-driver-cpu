# Linting

Run the linter against the codebase:

```bash
make lint
```

To automatically fix lint issues, pass `--fix` via the `GOLANGCI_LINT_EXTRA_ARGS` variable:

```bash
GOLANGCI_LINT_EXTRA_ARGS=--fix make lint
```

`GOLANGCI_LINT_EXTRA_ARGS` is forwarded verbatim to `golangci-lint run`, so any other supported flags can be passed the same way.
