//go:build !darwin || !cgo

package mygoui

import "github.com/getan/golder/internal/computeruse"

func installComputerPauseItem(*computeruse.Session) {}
