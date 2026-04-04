---
name: init
description: >-
  Bootstraps the Coroot workspace for local development (Go modules, frontend
  npm install, optional checks). Use when the user types /init, asks to
  initialize or set up the repo, bootstrap dependencies, or prepare a fresh
  clone for development.
---

# /init — workspace bootstrap

When the user invokes `/init` or asks to initialize this repository, run the workflow below **by executing commands** (do not only suggest them).

## Steps

1. **Go dependencies**
   - From the repo root: `go mod download` (or `make go-mod` if you need a tidy + verify).

2. **Frontend dependencies**
   - `cd front && npm ci`  
   - Matches `make npm-install` in the root `Makefile`.

3. **Sanity check (pick what fits time/context)**
   - Quick: `go build -o /dev/null .` from repo root, or
   - Full: `make test` and optionally `make lint` if the user wants a full gate.

4. **Optional: local stack**
   - If they need databases/services, point them at `deploy/docker-compose.yaml` and any env vars documented there or in project docs—do not invent secrets.

## Report back

Summarize what ran, exit codes, and the next command to start the app or UI (e.g. `go run .`, `npm run build-dev` in `front`) if known from `README`/`Makefile`.

## If something fails

Retry once with a clear hypothesis (network, Node version, Go version). If `go.mod` expects Go 1.23+, mention version mismatch when relevant.
