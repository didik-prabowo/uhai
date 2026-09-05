// Which tools may act on their own, and which have to ask. The classification
// is a property of the tool rather than of any front end: writing to disk and
// running commands escape this process, and looking does not.
package tools

// NeedsConfirm marks tools whose effects escape this process (writing to
// disk, running commands, leaving the machine) — callers must ask the user
// first.
//
// fetch_url is in the list for the direction nobody thinks about first. It
// reads, which sounds harmless, but it reads by *sending* — the URL goes to a
// third party, and a URL can carry whatever the model puts in it. Leaving it
// unasked would have made it the way around run_bash's confirmation: the same
// egress, through the tool that does not stop to ask. Allow it in settings if
// the asking is not worth it for you.
func NeedsConfirm(name string) bool {
	t, ok := find(name)
	return ok && t.NeedsConfirm()
}
