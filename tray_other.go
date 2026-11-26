//go:build !windows

package main

import "context"

func runTray(ctx context.Context, open, quit func()) {}
