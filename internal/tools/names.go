// The names the model calls tools by. They are the vocabulary three packages
// share — the schema sent to the provider, the permission rules a person
// writes in settings.json, and the questions the terminal asks — and until
// they were constants each of those spelled them out again. "run_bash" alone
// appeared thirteen times outside this package, so renaming a tool would have
// left the rules matching a name nothing answered to, and the confirmation
// showing raw JSON instead of the command. None of that fails a build.
package tools

const (
	NameRead       = "read_file"
	NameWrite      = "write_file"
	NameEdit       = "edit_file"
	NameGlob       = "glob"
	NameGrep       = "grep"
	NameBash       = "run_bash"
	NameFetch      = "fetch_url"
	NameFindSymbol = "find_symbol"
	NameList       = "list_directory"
	NamePlan       = "set_plan"
	NameSearch     = "search_web"

	// NameSpawnTask is here although the tool is not: spawning a task needs an
	// Agent, so it is built in internal/agent and never appears in Definitions.
	// The name is shared vocabulary all the same — agent declares the tool with
	// it and config resolves permission rules against it — and two spellings of
	// it is exactly the split that left "deny": ["spawn_task"] doing nothing.
	NameSpawnTask = "spawn_task"
)
