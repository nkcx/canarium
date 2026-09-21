// Recreate the placeholder that keeps Go's `//go:embed all:web/dist` pattern
// satisfiable on a clean checkout.
//
// Vite empties the output directory on every build (build.emptyOutDir
// defaults to true), which deletes the committed placeholder. Without this
// step, running the frontend build and then committing removes the file, and
// the next person to clone the repository cannot `go build ./...` at all.
//
// Runs automatically after `npm run build` via the postbuild hook.
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

const target = join(import.meta.dirname, '..', 'dist', '.gitkeep');

writeFileSync(
  target,
  `This file exists so \`//go:embed all:web/dist\` in web.go has something to
match on a clean checkout, before the frontend has been built.

Without it, \`go build ./...\`, \`go vet ./...\` and \`go test ./...\` all fail on a
newly cloned repository with:

    pattern all:web/dist: no matching files found

Vite empties this directory on every build, so web/scripts/keep-embed-placeholder.mjs
recreates the file afterwards. Real build output is gitignored; this file is
not. Do not delete it.
`,
);
