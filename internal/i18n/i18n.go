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

import "fmt"

var lang = "zh"

// SetLang selects the UI language; anything but "en" means Chinese.
func SetLang(l string) {
	if l == "en" {
		lang = "en"
		return
	}
	lang = "zh"
}

// Lang reports the active language ("zh" or "en").
func Lang() string { return lang }

// T translates and formats. With no args it never runs Sprintf, so a
// literal % in the copy cannot corrupt the output.
func T(key string, args ...any) string {
	s := key
	if lang == "en" {
		if v, ok := en[key]; ok {
			s = v
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}
