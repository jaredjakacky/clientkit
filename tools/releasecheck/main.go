// Command releasecheck validates that a proposed release version is canonical
// and compatible with the repository's Go module path.
package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "releasecheck: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: releasecheck <module-path> <version>")
	}

	return validateRelease(args[0], args[1])
}

func validateRelease(modulePath, version string) error {
	if modulePath == "" {
		return errors.New("module path must not be empty")
	}
	if version == "" {
		return errors.New("version must not be empty")
	}

	canonical := module.CanonicalVersion(version)
	if canonical == "" || canonical != version {
		return fmt.Errorf("version %q is not canonical Go module semver", version)
	}
	if build := semver.Build(version); build != "" {
		return fmt.Errorf("version %q must not contain build metadata", version)
	}
	if err := module.Check(modulePath, version); err != nil {
		return fmt.Errorf("module path %q is incompatible with version %q: %w", modulePath, version, err)
	}

	return nil
}
