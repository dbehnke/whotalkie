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
