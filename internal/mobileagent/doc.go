// Package mobileagent is the UI-neutral session boundary for native mobile
// hosts. Android (Kotlin) and a future iOS (Swift) host supply the workspace,
// provider stream, tool registry and authorization gate; this package owns one
// conversation, one run at a time, cancellation, and an ordered JSON-encodable
// event queue.
//
// It deliberately imports neither gui/appsession nor any desktop browser,
// terminal or Computer Use package. A host that wants the Android tool catalog
// builds it with internal/androidagent and passes the resulting Batch config.
package mobileagent
