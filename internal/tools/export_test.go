package tools

// unregister undoes a Register, so a test can add a tool without leaving it
// behind for the tests that check the built-in list against the docs.
func unregister(name string) {
	for i, t := range all {
		if t.Name() == name {
			all = append(all[:i], all[i+1:]...)
			return
		}
	}
}

// Unregister is the same, reached from the external test package that proves a
// tool can be written outside this one.
var Unregister = unregister
