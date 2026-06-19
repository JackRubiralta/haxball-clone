//go:build !linux

// On non-Linux platforms Ebiten uses a native windowing backend and never links
// github.com/jezek/xgb, so there are no X11 auth warnings to silence. This stub
// keeps the package importable (and the GUI binaries' blank import valid) on
// every OS while pulling in nothing. See x11quiet_linux.go for the real work.
package x11quiet
