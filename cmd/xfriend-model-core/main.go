package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/yetone/magpie/internal/claudebridge"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/pkg/modelcore"
)

func main() {
	if provider.TookOpenedURL(os.Args[1:]) {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "claude-mcp-helper" {
		if err := claudebridge.RunMCP(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		return
	}
	log.SetOutput(os.Stderr)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := modelcore.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		log.Print("managed model core ended with a protocol error")
		os.Exit(1)
	}
}
