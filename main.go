// gh-star-charts is a GitHub CLI extension that self-hosts star history
// charts: a one-time locally-authenticated backfill into an instance repo the
// user owns, then a secretless daily workflow keeps the charts current from
// the public star count.
package main

import (
	"os"

	"github.com/utkuozdemir/gh-star-charts/internal/cli"
)

var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.Run(os.Args[1:]))
}
