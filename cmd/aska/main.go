// Command aska installs agent skills from git repositories.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/pkgshed/aska/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "aska: error: %s\n", cli.FormatError(err))
		stop()
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	env, err := cli.SystemEnvironment()
	if err != nil {
		return err
	}
	return cli.Execute(ctx, os.Args[1:], env)
}
