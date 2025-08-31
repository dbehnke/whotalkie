CI for WhoTalkie

This repository includes a GitHub Actions workflow at `.github/workflows/go-test.yml`.

What the workflow does:
- Runs `golangci-lint` and `go vet` to catch lint and vet issues.
- Runs `go test ./...` with coverage and uploads the coverage file as an artifact.
- Builds the project to verify it compiles.
- Optionally uploads coverage to Codecov when a `CODECOV_TOKEN` secret is configured in the repository settings.

Enabling Codecov (optional):
1. Create an account and repository on Codecov.
2. Add `CODECOV_TOKEN` to this GitHub repo's Secrets (Settings → Secrets & variables → Actions).
3. The workflow will automatically upload coverage if the secret is present.

GitHub status checks will reflect the workflow job status. For stricter enforcement, require the `Go CI` job in branch protection rules.

Enforcing pure-Go server builds (branch protection)

This repository includes a dedicated workflow job named `Server: pure-Go build` which builds the server with `CGO_ENABLED=0` to ensure the server remains pure Go.

To require this check on PRs:

1. Go to your repository on GitHub → Settings → Branches → Branch protection rules.
2. Edit (or create) the protection rule for your target branch (e.g., `main`).
3. Under "Require status checks to pass before merging", add the exact status check name: `Server: pure-Go build`.
4. Save the rule. Pull requests that do not pass this check will be blocked from merging.

Note: The workflow job lives in `.github/workflows/go-test.yml` and will report the named status when runs complete.
