// Package lang selects the language of the core's user-facing messages.
//
// The app ships two UI languages (English and Russian) and follows the
// system / per-app language setting on the Android side; the Kotlin layer
// passes the active language code into the core at Start (and via
// mobile.SetLanguage), and every message the core returns to the UI is
// picked through T or Bilingual. Default before any Set call is Russian,
// matching the app's original behavior.
package lang

import (
	"strings"
	"sync/atomic"
)

var english atomic.Bool

// Set selects the message language from a language code ("en", "en-US", …
// → English; anything else → Russian).
func Set(code string) {
	english.Store(strings.HasPrefix(strings.ToLower(strings.TrimSpace(code)), "en"))
}

// En reports whether English is the selected language.
func En() bool { return english.Load() }

// T returns the string for the currently selected language.
func T(ru, en string) string {
	if english.Load() {
		return en
	}
	return ru
}

// bilingualError renders its message in the language selected at print
// time, so package-level sentinel errors stay comparable with errors.Is
// while still following later language switches.
type bilingualError struct{ ru, en string }

func (e *bilingualError) Error() string { return T(e.ru, e.en) }

// Bilingual creates an error whose message follows the selected language.
func Bilingual(ru, en string) error { return &bilingualError{ru: ru, en: en} }
