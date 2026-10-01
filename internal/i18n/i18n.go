// Package i18n is the app's minimal message catalog — two languages, no
// dependencies. The Chinese copy IS the key: T returns the key verbatim
// when the language is zh (the source language every string literal is
// written in) and the English translation from the en table otherwise,
// falling back to the key when a translation has not been written yet.
//
// Two rules keep this workable:
//
//   - Render-time text calls T inside View/render paths, so a live language
//     switch (settings window) repaints translated on the next frame.
//   - Transient messages (toasts) may translate at creation time — a toast
//     outliving the switch by a few seconds is accepted noise.
//
// Never store a T() result in a long-lived struct field.
package i18n

import (
	"fmt"
	"sync/atomic"
)

// lang is atomic, not a plain global: SetLang runs on the tea goroutine
// (the settings language picker) while T runs on every tea cmd goroutine
// (opDoneMsg labels and toasts are built inside those commands). A plain
// string header write races those reads, and the race detector CI runs
// flags it. T is the hot path (thousands of calls per frame), so it stays
// a single atomic load with no lock.
var lang atomic.Value

func init() { lang.Store("zh") }

// SetLang selects the UI language; anything but "en" means Chinese.
func SetLang(l string) {
	if l == "en" {
		lang.Store("en")
		return
	}
	lang.Store("zh")
}

// Lang reports the active language ("zh" or "en").
func Lang() string {
	if v, ok := lang.Load().(string); ok {
		return v
	}
	return "zh"
}

// T translates and formats. With no args it never runs Sprintf, so a
// literal % in the copy cannot corrupt the output.
func T(key string, args ...any) string {
	s := key
	if Lang() == "en" {
		if v, ok := en[key]; ok {
			s = v
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}
