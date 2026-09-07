// The lists and the forms: choosing a provider, typing its key, choosing a
// model, switching skills on and off. Everything here opens over the chat,
// takes one answer, and hands the prompt back.
package cli

import (
	"os"
	"regexp"
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/provider"
)

// updateCustomForm drives the three boxes. Tab and the arrows move between
// them, enter submits the lot — a form where enter meant "next" would need a
// separate key for "done", and there is nowhere obvious to put it.
func (m *teaModel) updateCustomForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = teaPrompt
		m.keyInput.Reset()
		m.input.Focus()
		return m, nil
	case "tab", "down":
		m.focusCustom(m.custom.focused + 1)
		return m, nil
	case "shift+tab", "up":
		m.focusCustom(m.custom.focused - 1)
		return m, nil
	case "enter":
		m.custom.values[m.custom.focused] = strings.TrimSpace(m.keyInput.Value())
		return m, m.submitCustom()
	}
	var cmd tea.Cmd
	m.keyInput, cmd = m.keyInput.Update(msg)
	return m, cmd
}

// focusCustom parks what is typed in the box it was typed in and opens the
// next, wrapping so tab alone can reach every field.
func (m *teaModel) focusCustom(to int) {
	m.custom.values[m.custom.focused] = strings.TrimSpace(m.keyInput.Value())
	m.custom.focused = (to + len(m.custom.values)) % len(m.custom.values)
	m.keyInput.SetValue(m.custom.values[m.custom.focused])
}

// beginCustom opens the form empty.
func (m *teaModel) beginCustom() tea.Cmd {
	m.custom = customForm{}
	m.provider = ""
	m.keyInput.Reset()
	m.keyInput.Focus()
	m.mode = teaCustomForm
	return nil
}

// submitCustom validates all three at once and saves them, then offers the
// models the endpoint turned out to have. A field that is wrong leaves the
// form up with the cursor on it, since retyping the other two would be the
// punishment for one typo.
func (m *teaModel) submitCustom() tea.Cmd {
	name := strings.ToLower(m.custom.values[customName])
	url := strings.TrimRight(m.custom.values[customURL], "/")
	key := m.custom.values[customKey]

	switch {
	case name == "":
		return m.customError(customName, "a name is needed — it is the prefix in provider/model")
	case key != "" && name == key:
		// The boxes are next to each other and one of them is masked, so this
		// is the mistake that happens. It has to be caught here rather than
		// left to look odd in the picker: the name goes to settings.json,
		// which is 0644 because it holds no secrets, and auth.json is 0600
		// because it does. A key in the wrong box lands in the wrong file.
		return m.customError(customName, "that is the key, not a name — the name is the prefix in provider/model")
	case !validProviderName(name):
		return m.customError(customName, "a name is up to 20 letters, digits, - or _ — no spaces, no slashes")
	case config.Known(name):
		return m.customError(customName, name+" is built in — point it elsewhere with \"baseUrls\" in settings.json")
	case url == "":
		return m.customError(customURL, "an endpoint is needed")
	case !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://"):
		return m.customError(customURL, "the base URL needs its scheme: http:// or https://")
	case key == "":
		return m.customError(customKey, "a key is needed — leave it out and every request comes back 401")
	}

	if err := config.SaveBaseURL(name, url); err != nil {
		return m.failCustom("could not save the endpoint: " + err.Error())
	}
	if err := config.Save(name, config.Creds{config.FieldKey: key}); err != nil {
		return m.failCustom("could not save the key: " + err.Error())
	}
	m.provider = name
	m.keyInput.Reset()
	m.addHistory(teaDim.Render("added " + name + " · " + url))
	// What it serves is the next question, and it is one this endpoint can be
	// asked. If it cannot, teaModelsMsg says so and the name can still be
	// typed: /model works on any provider/model, listed or not.
	return m.beginModelsFor(name)
}

// providerName is what a name may look like. Deliberately narrow: it is a map
// key, a settings field and the half of "provider/model" before the slash, and
// the one thing it must never be is long enough to be a credential.
var providerName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,19}$`)

func validProviderName(name string) bool { return providerName.MatchString(name) }

// customError puts the cursor back on the field that is wrong and says why.
func (m *teaModel) customError(field int, reason string) tea.Cmd {
	m.custom.focused = field
	m.keyInput.SetValue(m.custom.values[field])
	m.addHistory(teaDim.Render(reason))
	return nil
}

func (m *teaModel) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode == teaCustomForm {
		return m.updateCustomForm(msg)
	}
	// Key entry is read before the general escape below: backing out of a
	// provider that already has a key means keeping that key, not abandoning
	// the connection.
	if m.mode == teaKeyEntry {
		switch msg.String() {
		case "esc":
			m.keyInput.Reset()
			if customStep(m.credField) {
				// Half a custom provider cannot be activated, and activating
				// the old one would be a surprise. Whatever was answered so
				// far is saved and /connect can pick it up again.
				m.mode = teaPrompt
				m.input.Focus()
				return m, nil
			}
			if m.credField != config.FieldKey {
				return m, m.activateProvider(m.provider) // the key is in already
			}
			if config.APIKey(m.provider) == "" {
				m.mode = teaPrompt
				m.input.Focus()
				return m, nil
			}
			return m, m.activateProvider(m.provider)
		case "enter":
			return m, m.saveCredential(strings.TrimSpace(m.keyInput.Value()))
		}
		var cmd tea.Cmd
		m.keyInput, cmd = m.keyInput.Update(msg)
		return m, cmd
	}
	// A list being searched owns the keyboard, with one exception. Esc has to
	// clear the search rather than close the picker — otherwise a typo costs
	// the whole list — and space has to be a space rather than the skill
	// picker's toggle. Enter is the exception: bubbletea would apply the
	// filter and wait, when the row is already under the cursor and the
	// answer is obviously "that one".
	if m.picker.FilterState() != list.Unfiltered && msg.String() != "enter" {
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		return m, cmd
	}
	if msg.String() == "esc" {
		m.mode = teaPrompt
		m.input.Focus()
		return m, nil
	}
	if key := msg.String(); key == "enter" || (key == " " && m.mode == teaSkillPicker) {
		item := m.picker.SelectedItem()
		if item == nil {
			return m, nil
		}
		selected := item.(teaItem).title
		switch m.mode {
		case teaProviderPicker:
			return m, m.selectProvider(selected)
		case teaDisconnectPicker:
			m.mode = teaPrompt
			m.input.Focus()
			return m, m.disconnect(selected)
		case teaSkillPicker:
			return m, m.toggleSkill(selected)
		}
		return m, m.changeModel(selected, false)
	}
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)
	return m, cmd
}

// beginSkills opens the list of skills so they can be switched on and off in
// it. A picker rather than a printed report because switching one off means
// looking at what it costs first, and those are the same rows.
func (m *teaModel) beginSkills() tea.Cmd {
	rows := skillRows()
	if len(rows) == 0 {
		m.addHistory(skillsReport()) // nothing to pick from; say where it looked
		return nil
	}
	items := make([]list.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, teaItem{title: row.name, desc: row.desc})
	}
	m.picker = m.newPicker(items, "Skills — enter to switch on or off, esc to close")
	m.mode = teaSkillPicker
	return nil
}

// toggleSkill switches one, writes it to the project's settings.json, and
// rebuilds the rows in place: the list stays open because switching one off is
// rarely the only one, and the cost line has to move as it happens.
func (m *teaModel) toggleSkill(name string) tea.Cmd {
	was := skillIsOff(name)
	if err := config.SetSkillOff(name, !was); err != nil {
		m.mode = teaPrompt
		m.input.Focus()
		m.addHistory(teaDim.Render("could not save: " + err.Error()))
		return nil
	}
	if now := skillIsOff(name); now == was {
		// Off accumulates across files: the project cannot switch on what the
		// home settings turned off, and saying nothing would look like a bug.
		m.mode = teaPrompt
		m.input.Focus()
		m.addHistory(teaDim.Render(name + " is switched off in ~/.uhai/settings.json — edit it there"))
		return nil
	}

	index := m.picker.Index()
	items := make([]list.Item, 0)
	for _, row := range skillRows() {
		items = append(items, teaItem{title: row.name, desc: row.desc})
	}
	m.picker.SetItems(items)
	m.picker.Select(index)
	return nil
}

// customRow is the last line of the provider picker, and the only item there
// that is not a provider. Spelled with a space so it cannot collide with a
// name: providers are one word, and connectCustomName refuses anything else.
const customRow = "+ custom endpoint"

// The steps of the custom form, asked in this order. They are not credentials
// and never reach auth.json — a name is a map key, a base URL is a setting —
// but they travel through askCredential because that is already the machinery
// for asking one thing at a time, and a second one would be the same code
// wearing a different name.
// fieldCustomModel is the one question the form does not ask. It comes up when
// a custom provider is connected again and the endpoint cannot list what it
// serves — there is nothing to pick from, so the id gets typed.
const fieldCustomModel = "customModel"

func customStep(field string) bool { return field == fieldCustomModel }

func (m *teaModel) beginConnect(arg string) tea.Cmd {
	if arg != "" {
		return m.selectProvider(arg)
	}
	items := make([]list.Item, 0)
	for _, item := range providerItems() {
		items = append(items, teaItem{title: item.name, desc: item.desc})
	}
	m.picker = m.newPicker(items, "Connect provider")
	m.mode = teaProviderPicker
	return nil
}

// askCredential puts the entry box in front of one field. Providers are asked
// for their key first and their extras after, one question at a time, because
// a form that asks for three things at once has to explain all three before
// any of them can be typed.
func (m *teaModel) askCredential(field string) {
	m.credField = field
	m.keyInput.Reset()
	saved := config.Get(m.provider)[field] != ""
	switch {
	case field == fieldCustomModel:
		m.keyInput.Placeholder = "model id, exactly as the endpoint spells it"
	case field == config.FieldKey && saved:
		m.keyInput.Placeholder = "paste a new API key, or esc to keep the one saved"
	case field == config.FieldKey:
		m.keyInput.Placeholder = "paste API key"
	case saved:
		m.keyInput.Placeholder = "enter keeps the one saved"
	default:
		m.keyInput.Placeholder = "enter to skip — only identity-linked keys need one"
	}
	m.keyInput.Focus()
	m.mode = teaKeyEntry
}

// saveCredential stores what was typed, then asks for the next field or
// connects. An empty answer is only allowed for the extras: a provider with no
// key cannot be connected at all, so an empty one leaves the question up.
func (m *teaModel) saveCredential(value string) tea.Cmd {
	// Every step of the custom form is required — a nameless provider or one
	// with no endpoint is not half-configured, it is nothing — so an empty
	// answer leaves the question up, the way an empty key does.
	if value == "" && (m.credField == config.FieldKey || customStep(m.credField)) {
		return nil
	}
	if customStep(m.credField) {
		return m.saveCustomStep(value)
	}
	if value != "" {
		if err := config.Save(m.provider, config.Creds{m.credField: value}); err != nil {
			m.addHistory(teaDim.Render("could not save " + credLabel(m.credField) + ": " + err.Error()))
			m.mode = teaPrompt
			m.input.Focus()
			return nil
		}
		// The environment beats the file, so saving while one is exported
		// looks like nothing happened.
		if env := config.EnvVar(m.provider, m.credField); env != "" {
			m.addHistory(teaDim.Render(env + " is set and wins over what is saved — unset it for this to take effect"))
		}
	}
	m.keyInput.Reset()
	if m.credField == config.FieldKey {
		if extra := config.ExtraFields(m.provider); len(extra) > 0 {
			m.askCredential(extra[0])
			return nil
		}
		// A provider from the table brings a default model; one somebody just
		// added brings nothing, and activateProvider has nothing to load.
		if config.DefaultModel(m.provider) == "" {
			m.askCredential(fieldCustomModel)
			return nil
		}
	}
	return m.activateProvider(m.provider)
}

// saveCustomStep advances the custom form: a name, then an endpoint, then the
// key and the model that the ordinary path already knows how to ask for.
// saveCustomStep is the model id, asked only when the endpoint could not be
// listed. The name, the URL and the key are the form's business.
func (m *teaModel) saveCustomStep(value string) tea.Cmd {
	if err := config.SaveModel(m.provider + "/" + strings.TrimSpace(value)); err != nil {
		return m.failCustom("could not save model: " + err.Error())
	}
	p, err := config.LoadProvider()
	if err != nil {
		return m.failCustom("could not connect: " + err.Error())
	}
	m.keyInput.Reset()
	m.mode = teaPrompt
	m.input.Focus()
	m.useProvider(p)
	m.addHistory(teaDim.Render("using " + p.Name()))
	return nil
}

// failCustom abandons the form with a reason. What was written stays written:
// a saved base URL with no key is visible in /connect and can be finished,
// which beats making somebody type the URL again.
func (m *teaModel) failCustom(reason string) tea.Cmd {
	m.keyInput.Reset()
	m.mode = teaPrompt
	m.input.Focus()
	m.addHistory(teaDim.Render(reason))
	return nil
}

// credLabel is what a credential is called on screen.
func credLabel(field string) string {
	if field == config.FieldWorkspace {
		return "workspace id"
	}
	return "API key"
}

// disconnect forgets a provider's saved key, the half of /connect that was
// missing: a key could go in and nothing could take one out, so a provider
// stayed in /model for good once it had been tried once.
//
// With no argument it offers the connected ones, since those are the only
// ones there is anything to forget about.
func (m *teaModel) disconnect(name string) tea.Cmd {
	if name == "" {
		var items []list.Item
		for _, p := range config.ConnectedProviders() {
			if config.NeedsKey(p) {
				items = append(items, teaItem{title: p, desc: "forget the saved key"})
			}
		}
		if len(items) == 0 {
			m.addHistory(teaDim.Render("nothing to disconnect — no provider has a saved key"))
			return nil
		}
		m.picker = m.newPicker(items, "Disconnect provider")
		m.mode = teaDisconnectPicker
		return nil
	}

	if !config.Configured(name) {
		m.addHistory(teaDim.Render("unknown provider: " + name))
		return nil
	}
	// A custom provider is a base URL and a key. Forgetting only the key
	// leaves it in every picker as a provider that cannot answer, so it goes
	// whole — the table entries have nothing to take away.
	custom := !config.Known(name)
	if custom {
		if err := config.ForgetBaseURL(name); err != nil {
			m.addHistory(teaDim.Render("could not remove the endpoint: " + err.Error()))
			return nil
		}
	}
	had, err := config.Forget(name)
	if err != nil {
		m.addHistory(teaDim.Render("could not disconnect " + name + ": " + err.Error()))
		return nil
	}
	if !had && !custom {
		m.addHistory(teaDim.Render(name + " has no saved key"))
		return nil
	}
	m.addHistory(teaDim.Render("disconnected " + name))

	// Saying "disconnected" while an exported key still works would be a lie,
	// and it is the same trap /connect warns about from the other side.
	if env := config.EnvVar(name, config.FieldKey); env != "" && os.Getenv(env) != "" {
		m.addHistory(teaDim.Render(env + " is still set, so " + name + " keeps working — unset it to finish"))
	}
	return nil
}

func (m *teaModel) selectProvider(name string) tea.Cmd {
	if name == customRow {
		return m.beginCustom()
	}
	// Configured rather than Known: a gateway added last week has no table
	// entry and is still a provider you can connect to.
	if !config.Configured(name) {
		m.addHistory(teaDim.Render("unknown provider: " + name + " — use " + customRow + " in /connect to add one"))
		m.mode = teaPrompt
		return nil
	}
	// Always offered, even when a key is saved: a key that is refused is
	// exactly the one you need to replace, and there was no way to.
	if config.NeedsKey(name) {
		m.provider = name
		m.askCredential(config.FieldKey)
		return nil
	}
	return m.activateProvider(name)
}

func (m *teaModel) activateProvider(name string) tea.Cmd {
	modelName := config.DefaultModel(name)
	if modelName == "" {
		m.addHistory(teaDim.Render("no default model configured for " + name))
		m.mode = teaPrompt
		return nil
	}
	if err := config.SaveModel(name + "/" + modelName); err != nil {
		m.addHistory(teaDim.Render("could not save model: " + err.Error()))
		m.mode = teaPrompt
		return nil
	}
	p, err := config.LoadProvider()
	if err != nil {
		m.addHistory(teaDim.Render("could not connect: " + err.Error()))
		m.mode = teaPrompt
		return nil
	}
	m.useProvider(p)
	m.mode = teaPrompt
	m.input.Focus()
	m.addHistory(teaDim.Render("connected to " + p.Name()))
	return nil
}

// useProvider switches model, and with it how much history fits before the
// agent has to summarize — a window is a property of the model, not a setting.
func (m *teaModel) useProvider(p provider.Provider) {
	m.agent.Provider = p
	m.agent.MaxContextTokens = config.ContextWindow(p.Name())
	m.agent.UseTools = config.SupportsTools(p.Name())
}

func (m *teaModel) beginModels() tea.Cmd { return m.beginModelsFor("") }

// beginModelsFor is the same list narrowed to one provider, which is what
// /connect opens after adding a gateway: the models just made reachable are
// the answer to "and now what", and offering every provider's would bury them.
func (m *teaModel) beginModelsFor(only string) tea.Cmd {
	m.status = m.spinner.View() + " loading models..."
	return func() tea.Msg {
		items, err := loadModelItems(only)
		return teaModelsMsg{items: items, err: err}
	}
}

func loadModelItems(only string) ([]list.Item, error) {
	// models.dev is fetched here rather than at startup: this is the one place
	// its figures are read, and it already runs off the UI's thread behind
	// "loading models...". A session that never opens the picker never pays
	// for it, and one opened offline falls back to the table in models.go —
	// which is why the error is dropped rather than shown.
	_ = config.RefreshCatalog()

	var items []list.Item
	for _, name := range config.ConnectedProviders() {
		if only != "" && name != only {
			continue
		}
		// Any model will do: the client is built to be asked what exists, and
		// a custom provider has no default to build it with.
		p, err := config.LoadProviderFor(name + "/" + config.ListerModel(name))
		if err != nil {
			continue
		}
		lister, ok := p.(provider.ModelLister)
		if !ok {
			continue
		}
		models, err := config.CachedModels(name, lister)
		if err != nil {
			continue
		}
		// One provider, one block, ranked inside it. The six best of each
		// provider used to go first and every remainder after them, so the
		// top of the picker kept the shape it had before custom providers
		// existed. That cost more than it bought the moment a gateway
		// arrived with twenty models: six of them showed at the top and the
		// other fourteen sat below three providers' worth of rows, which
		// reads as "they are missing" rather than "they are further down".
		//
		// Anything past the fold is reached with "/" — which is also why
		// every model has to be *in* the list: bubbletea's filter searches
		// the items it was given and nothing else.
		for _, modelName := range recommended(name, models) {
			setting := name + "/" + modelName
			items = append(items, teaItem{title: setting, desc: config.ModelSummary(setting)})
		}
	}
	return items, nil
}

// recommended is a provider's models in the order the picker wants them: the
// ones uhai has figures for first, then the rest as the provider sent them.
// The order used to be the provider's own and the list was cut to the first
// six of it, which is the head of an order no vendor documents.
//
// It ranks and no longer discards, because a search that cannot reach a model
// is not a search. A model the endpoint never lists is still only reachable by
// typing its name, which /model accepts from anyone.
func recommended(name string, models []string) []string {
	live := make([]string, 0, len(models))
	for _, m := range models {
		if config.Recommendable(name + "/" + m) {
			live = append(live, m)
		}
	}
	// A dated id and its alias are one model listed twice, which is how OpenAI
	// spent all six slots on gpt-4.1: the plain name, the dated one, and the
	// same pair again for -mini and -nano. Dropped only when the undecorated
	// name is in the same reply — Anthropic lists nothing but dated ids, and
	// dropping those would leave it with no models at all.
	named := make(map[string]bool, len(live))
	for _, m := range live {
		named[m] = true
	}
	var once []string
	for _, m := range live {
		if plain := undecorated(m); plain == m || !named[plain] {
			once = append(once, m)
		}
	}

	// Everything filtered out says the table is wrong about this provider, not
	// that the provider has nothing to offer, so fall back to the whole list.
	if len(once) > 0 {
		models = once
	}

	sort.SliceStable(models, func(i, j int) bool {
		return config.KnownModel(name+"/"+models[i]) && !config.KnownModel(name+"/"+models[j])
	})
	return models
}

// stamp matches the release date vendors bolt onto a model id, in each of the
// three shapes they use: 2025-04-14, 20260115, 10-2025.
var stamp = regexp.MustCompile(`-(?:\d{4}-\d{2}-\d{2}|\d{8}|\d{2}-\d{4})$`)

// undecorated strips the date and the "-preview" that mark a release of a
// model rather than a different model, so gpt-4.1-2025-04-14 can be recognised
// as gpt-4.1 and gemini-3.1-flash-lite-preview as gemini-3.1-flash-lite. Both
// at once and repeatedly, since they arrive stacked.
func undecorated(name string) string {
	for {
		switch {
		case stamp.MatchString(name):
			name = stamp.ReplaceAllString(name, "")
		case strings.HasSuffix(name, "-preview"):
			name = strings.TrimSuffix(name, "-preview")
		default:
			return name
		}
	}
}

// verify says whether the name is worth checking against the provider's list.
// A name picked from that same list a moment ago is not: the check would spend
// a second round-trip to confirm a match it cannot fail.
func (m *teaModel) changeModel(setting string, verify bool) tea.Cmd {
	// Whatever happens next, the picker is done: every path below reports
	// through the history, which is only visible on the chat page.
	m.mode = teaPrompt
	m.input.Focus()
	if setting == "" {
		m.addHistory(teaDim.Render("use /model provider/model, for example /model zai/glm-4.7"))
		return nil
	}
	p, err := config.LoadProviderFor(setting)
	if err != nil {
		m.addHistory(teaDim.Render("could not load model: " + err.Error()))
		return nil
	}
	if err := config.SaveModel(setting); err != nil {
		m.addHistory(teaDim.Render("could not save model: " + err.Error()))
		return nil
	}
	m.useProvider(p)
	m.addHistory(teaDim.Render("using " + p.Name()))
	if !verify {
		return nil
	}
	return checkModelExists(p, setting)
}

// checkModelExists asks the provider whether it has heard of the model, after
// the switch rather than before it.
//
// After, and as a note rather than a refusal, because a list is not the truth:
// Z.ai answers /models with ten paid models and none of the free ones, which
// work perfectly well. Refusing what the vendor forgot to list would be a
// typo check that blocks a working model. A typo, meanwhile, otherwise shows
// up as a failed turn one prompt later, with an error about the model that
// never mentions the spelling.
func checkModelExists(p provider.Provider, setting string) tea.Cmd {
	return func() tea.Msg {
		lister, ok := p.(provider.ModelLister)
		if !ok {
			return nil // nothing to check against
		}
		name, _, _ := strings.Cut(setting, "/")
		models, err := config.CachedModels(name, lister)
		if err != nil || len(models) == 0 {
			return nil // could not ask; silence beats a guess
		}

		note := unlistedNote(setting, models)
		if note == "" {
			return nil
		}
		return teaNoteMsg(note)
	}
}

// unlistedNote is what to say about a model the provider did not list, and ""
// when it did.
func unlistedNote(setting string, models []string) string {
	name, want, ok := strings.Cut(setting, "/")
	if !ok || want == "" {
		return ""
	}
	for _, listed := range models {
		// A prefix counts as listed. An alias is never in the list itself,
		// only the dated id it points at: Anthropic answers /models with
		// claude-sonnet-5-20260115 and never with claude-sonnet-5, which is
		// the name uhai recommends and ships as its own default.
		if len(listed) >= len(want) && strings.EqualFold(listed[:len(want)], want) {
			return ""
		}
	}
	return want + " is not in the list " + name + " returns — it may still work, " +
		"but check the spelling if the next turn fails"
}
