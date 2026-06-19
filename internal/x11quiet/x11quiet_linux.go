//go:build linux

// Package x11quiet silences the two harmless X11 auth warnings that
// github.com/jezek/xgb prints on Linux when no Xauthority cookie is found:
//
//	XGB: conn.go:47: Could not get authority info: open ~/.Xauthority: no such file or directory
//	XGB: conn.go:48: Trying connection without authority info...
//
// This happens in a Wayland session (XWayland, no exported XAUTHORITY) on every
// launch. The local connection succeeds regardless, so the warnings are pure
// noise.
//
// Timing matters: Ebiten opens its X connection inside the internal/ui package's
// own init() (newUserInterface -> initializeGLFW -> monitor enumeration), which
// Go runs before any package main init(). To silence it in time, this package
// imports xgb WITHOUT importing Ebiten, and the GUI binaries blank-import it in
// their FIRST import group so it initializes before Ebiten's package tree. An
// empty ~/.Xauthority does not help (xgb treats the resulting EOF as an error
// and warns anyway), so we mute xgb's logger directly, exactly as xgb documents
// on its Logger var.
//
// This file is compiled only on Linux; Windows and macOS use native backends and
// never link xgb (see x11quiet_other.go), so the package is a cross-platform
// no-op everywhere else. Real connection failures still surface as errors from
// ebiten.RunGame, not via this logger.
package x11quiet

import (
	"io"
	"log"

	"github.com/jezek/xgb"
)

func init() { xgb.Logger = log.New(io.Discard, "", 0) }
