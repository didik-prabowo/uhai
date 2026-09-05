// The lists and the forms: choosing a provider, typing its key, choosing a
// model, switching skills on and off. Everything here opens over the chat,
// takes one answer, and hands the prompt back.
package cli

import (
	"regexp"
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/provider"
)

func (m *teaModel) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Key entry is read before the general escape below: backing out of a
	// provider that already has a key means keeping that key, not abandoning
	// the connection.
	if m.mode == teaKeyEntry {
		switch msg.String() {
		case "esc":
			m.keyInput.Reset()
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
	if value == "" && m.credField == config.FieldKey {
		return nil
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
	}
	return m.activateProvider(m.provider)
}

// credLabel is what a credential is called on screen.
func credLabel(field string) string {
	if field == config.FieldWorkspace {
		return "workspace id"
	}
	return "API key"
}

func (m *teaModel) selectProvider(name string) tea.Cmd {
	if !config.Known(name) {
		m.addHistory(teaDim.Render("unknown provider: " + name))
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

func (m *teaModel) beginModels() tea.Cmd {
	m.status = m.spinner.View() + " loading models..."
	return func() tea.Msg {
		items, err := loadModelItems()
		return teaModelsMsg{items: items, err: err}
	}
}

func loadModelItems() ([]list.Item, error) {
	var items []list.Item
	for _, name := range config.ConnectedProviders() {
		modelName := config.DefaultModel(name)
		if modelName == "" {
			continue
		}
		p, err := config.LoadProviderFor(name + "/" + modelName)
		if err != nil {
			continue
		}
		lister, ok := p.(provider.ModelLister)
		if !ok {
			continue
		}
		models, err := lister.Models()
		if err != nil {
			continue
		}
		for _, modelName := range recommended(name, models) {
			setting := name + "/" + modelName
			items = append(items, teaItem{title: setting, desc: config.ModelSummary(setting)})
		}
	}
	return items, nil
}

// recommended is the handful of a provider's models the picker shows, the ones
// uhai has figures for first. The cut used to be the first six the provider
// returned, which is the head of an order no vendor documents.
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
	if len(models) > maxRecommendedModelsPerProvider {
		models = models[:maxRecommendedModelsPerProvider]
	}
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
		models, err := lister.Models()
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
