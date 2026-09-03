// Which tools may act on their own, and which have to ask. The classification
// is a property of the tool rather than of any front end: writing to disk and
// running commands escape this process, and looking does not.
package tools

// NeedsConfirm marks tools whose effects escape this process (writing to
// disk, running commands) — callers must ask the user first.
func NeedsConfirm(name string) bool {
	return name == "write_file" || name == "edit_file" || name == "run_bash"
}
