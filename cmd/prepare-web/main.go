// prepare-web installs the browser archive pinned by a reviewed local manifest.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ZenNotes/zennotes/apps/server/internal/webartifact"
)

func main() {
	manifest := flag.String("manifest", "", "path to the pinned browser artifact manifest")
	archive := flag.String("archive", "", "optional local archive (otherwise use the adjacent file or manifest HTTPS URL)")
	output := flag.String("output", "", "new output directory, typically web/dist in a clean build")
	allowDirty := flag.Bool("allow-dirty", false, "allow uncommitted source candidates for local testing")
	flag.Parse()
	if *manifest == "" || *output == "" || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := webartifact.Install(context.Background(), *manifest, *archive, *output, *allowDirty); err != nil {
		fmt.Fprintln(os.Stderr, "prepare-web:", err)
		os.Exit(1)
	}
	fmt.Println("Verified browser artifact installed:", *output)
}
