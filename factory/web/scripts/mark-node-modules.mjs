import { writeFile } from "node:fs/promises";

// The repository root is a Go module. A few npm packages contain incidental
// .go files, so mark node_modules as a nested module and keep `go test ./...`
// focused on Factory packages after npm install.
await writeFile(
  new URL("../node_modules/go.mod", import.meta.url),
  "module github.com/owainlewis/factory/web/node_modules\n\ngo 1.25\n",
);
