// Package desktop is the module root for the TwoPlacePaste desktop tray
// service (Windows and macOS).
//
// The service is a shell around
// github.com/Mark7888/two-place-paste/pkg/tppclient: every key, every frame
// and every flow of SPEC §3 and §6 lives there, and nothing in this module
// re-derives any of it. What this module owns is the parts a shared client
// core must not have — the OS clipboard, a tray icon, an autostart entry, and
// a localhost HTTP server that serves the React UI to the user's browser
// (SPEC §7.2).
//
// The binary is cmd/tppdesktop; internal/service wires the pieces together.
package desktop
