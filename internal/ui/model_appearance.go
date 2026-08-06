package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/kranz-org/kranz/internal/config"
	usersettings "github.com/kranz-org/kranz/internal/settings"
)

// Appearance resolution and the live theme picker. Theme, accent, background
// ownership, and colour mode are four independent choices, and this file owns
// how they are resolved, previewed, and persisted.

// themeAccentSource names where the picker's accent comes from. It replaces a
// pair of independent fields that could both describe the accent at once — a
// personal custom colour alongside a project accent — leaving every reader to
// resolve the contradiction with its own precedence chain.
type themeAccentSource uint8

const (
	// themeAccentSourceTheme keeps whatever accent the selected theme defines.
	themeAccentSourceTheme themeAccentSource = iota
	// themeAccentSourceProject pins the accent declared by the project config.
	themeAccentSourceProject
	// themeAccentSourceCustom pins themeCustomAccent, typed in the editor or
	// carried over from the user settings.
	themeAccentSourceCustom
)

func effectiveAppearance(project config.UIConfig, user usersettings.Settings) (theme, accent, background, colorMode string) {
	theme = project.Theme
	accent = project.Accent
	background = normalizeBackgroundSource(project.Background)
	colorMode = normalizeColorMode(project.ColorMode)
	if theme == "" {
		theme = DefaultTheme
	}
	if user.Theme != "" && user.Theme != "auto" {
		theme = user.Theme
		// A user-selected theme uses its own palette. The project's accent only
		// belongs to the project's theme and must not make every preview blue.
		accent = ""
	}
	if user.Accent == "theme" {
		accent = ""
	} else if user.Accent != "" && user.Accent != "auto" {
		accent = user.Accent
	}
	if user.Background != "" {
		background = normalizeBackgroundSource(user.Background)
	}
	if user.ColorMode != "" {
		colorMode = normalizeColorMode(user.ColorMode)
	}
	return theme, accent, background, colorMode
}

func normalizeBackgroundSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case backgroundTheme:
		return backgroundTheme
	default:
		return backgroundTerminal
	}
}

func normalizeColorMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case colorModeDark:
		return colorModeDark
	case colorModeLight:
		return colorModeLight
	default:
		return colorModeAuto
	}
}

func colorModeIsDark(mode string, terminalDark bool) bool {
	switch normalizeColorMode(mode) {
	case colorModeDark:
		return true
	case colorModeLight:
		return false
	default:
		return terminalDark
	}
}

func applyAppearance(name, accent, background, colorMode string, terminalDark bool) (Theme, error) {
	return ApplyThemeVariant(
		name,
		accent,
		colorModeIsDark(colorMode, terminalDark),
		normalizeBackgroundSource(background) == backgroundTerminal,
	)
}

func newThemeAccentInput() textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	// The six-digit limit is enforced by sanitizeThemeAccentInput instead of
	// CharLimit. A limit here would truncate before sanitizing, so pasting the
	// seven-character "#FF0000" would keep "#FF000" and lose the last digit;
	// sanitizing first drops the "#" and leaves all six digits.
	input.CharLimit = 0
	input.Width = 6
	return input
}

func (m *Model) pollSystemAppearance() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		dark, available := detectSystemDarkMode()
		return systemAppearanceMsg{dark: dark, available: available}
	})
}

func (m *Model) probeTerminalBackground(force bool) tea.Cmd {
	if !terminalBackgroundProbeSupported() || m.backgroundProbeBusy || (!force && time.Since(m.lastBackgroundProbe) < time.Second) {
		return nil
	}
	m.backgroundProbeBusy = true
	probe := &terminalBackgroundProbe{}
	return tea.Exec(probe, func(err error) tea.Msg {
		return backgroundColorMsg{dark: probe.dark, err: err}
	})
}

func (m *Model) applyDetectedBackground(dark bool, source string) tea.Cmd {
	if dark == m.terminalDark {
		return nil
	}
	m.terminalDark = dark
	_, _, _, colorMode := effectiveAppearance(m.cfg.UI, m.userSettings)
	if m.mode == ModeThemes {
		colorMode = m.themeColorMode
	}
	if colorMode != colorModeAuto {
		return nil
	}
	if m.mode == ModeThemes {
		m.previewThemePicker()
	} else if err := m.applyEffectiveAppearance(); err != nil {
		m.addNotification("appearance", "Could not adapt to terminal background: "+err.Error(), config.LogError)
		return nil
	}
	mode := "light"
	if dark {
		mode = "dark"
	}
	m.addNotification("appearance", source+" appearance changed to "+mode, config.LogInfo)
	return tea.ClearScreen
}

func (m *Model) openThemePicker() {
	m.themeBefore = m.activeTheme
	m.settingsBefore = m.userSettings
	m.syncThemePickerControls()
	m.mode = ModeThemes
}

func (m *Model) syncThemePickerControls() {
	m.themeAccentEditing = false
	m.themeAccentReplace = false
	m.themeAccentError = ""
	m.themeAccentInput.Blur()
	m.themeCursor = 0
	for index, name := range ThemeNames() {
		if name == m.activeTheme.Name {
			m.themeCursor = index
			break
		}
	}
	m.themeUseProject = m.userSettings.Theme == "" || m.userSettings.Theme == "auto"
	projectAccent := strings.TrimSpace(m.cfg.UI.Accent)
	m.themeAccentChanged = false
	m.themeCustomAccent = ""
	// A personal accent that differs from the project's is a custom one, and it
	// takes the source outright: the project accent it overrides is not a second
	// simultaneous answer.
	switch {
	case isCustomAccent(m.userSettings.Accent, m.cfg.UI.Accent):
		m.themeCustomAccent = strings.ToUpper(strings.TrimSpace(m.userSettings.Accent))
		m.themeAccentSource = themeAccentSourceCustom
	case projectAccent != "" && m.userSettings.Accent != "theme" &&
		(m.themeUseProject || strings.EqualFold(m.userSettings.Accent, projectAccent)):
		m.themeAccentSource = themeAccentSourceProject
	default:
		m.themeAccentSource = themeAccentSourceTheme
	}
	_, _, m.themeBackground, m.themeColorMode = effectiveAppearance(m.cfg.UI, m.userSettings)
}

func (m *Model) handleThemeKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.themeAccentEditing {
		return m.handleThemeAccentKeys(msg)
	}
	names := ThemeNames()
	switch msg.String() {
	case "up", "k":
		m.themeCursor = (m.themeCursor - 1 + len(names)) % len(names)
		m.themeUseProject = false
		m.previewThemePicker()
	case "down", "j":
		m.themeCursor = (m.themeCursor + 1) % len(names)
		m.themeUseProject = false
		m.previewThemePicker()
	case "enter":
		m.applyThemePicker(names)
	case "r", "R":
		m.reloadSavedAppearance()
	case "g", "G":
		m.saveThemePicker(names)
	case "c", "C":
		m.saveThemePickerToProject()
	case "p", "P":
		m.themeUseProject = !m.themeUseProject
		m.previewThemePicker()
	case "A":
		return m, m.beginThemeAccentEdit()
	case "a":
		return m, m.toggleThemeAccentSource()
	case "b", "B":
		m.toggleThemeBackgroundSource()
	case "m", "M":
		m.cycleThemeColorMode()
	case "esc", "q":
		m.cancelThemePicker()
	}
	return m, nil
}

func (m *Model) beginThemeAccentEdit() tea.Cmd {
	value := strings.TrimPrefix(strings.TrimSpace(m.activeTheme.Accent), "#")
	m.themeAccentInput.SetValue(strings.ToUpper(value))
	m.themeAccentInput.CursorEnd()
	m.themeAccentError = ""
	m.themeAccentEditing = true
	m.themeAccentReplace = true
	return m.themeAccentInput.Focus()
}

func (m *Model) handleThemeAccentKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.themeAccentInput.Blur()
		m.themeAccentEditing = false
		m.themeAccentReplace = false
		m.themeAccentError = ""
		return m, nil
	case "enter":
		accent := "#" + strings.ToUpper(m.themeAccentInput.Value())
		if !hexColorPattern.MatchString(accent) {
			m.themeAccentError = "Enter 6 hex digits"
			return m, nil
		}
		m.themeCustomAccent = accent
		m.themeAccentChanged = true
		m.themeAccentSource = themeAccentSourceCustom
		m.themeAccentInput.Blur()
		m.themeAccentEditing = false
		m.themeAccentReplace = false
		m.themeAccentError = ""
		m.previewThemePicker()
		return m, nil
	case "tab", "shift+tab":
		return m, nil
	}

	if msg.Type == tea.KeyRunes {
		value := sanitizeThemeAccentValue(string(msg.Runes))
		if value == "" {
			return m, nil
		}
		if m.themeAccentReplace {
			m.themeAccentInput.SetValue("")
			m.themeAccentInput.CursorStart()
		}
		m.themeAccentReplace = false
		msg.Runes = []rune(value)
	} else {
		if msg.String() == "ctrl+v" && m.themeAccentReplace {
			m.themeAccentInput.SetValue("")
			m.themeAccentInput.CursorStart()
		}
		m.themeAccentReplace = false
	}
	m.themeAccentError = ""
	var command tea.Cmd
	m.themeAccentInput, command = m.themeAccentInput.Update(msg)
	m.sanitizeThemeAccentInput()
	return m, command
}

// sanitizeThemeAccentInput keeps the field at six hexadecimal digits. Byte
// slicing and byte lengths are safe against the rune cursor position only
// because sanitizeThemeAccentValue admits ASCII hex digits and nothing else.
func (m *Model) sanitizeThemeAccentInput() {
	value := m.themeAccentInput.Value()
	position := m.themeAccentInput.Position()
	sanitized := sanitizeThemeAccentValue(value)
	if len(sanitized) > 6 {
		sanitized = sanitized[:6]
	}
	if sanitized == value {
		return
	}
	m.themeAccentInput.SetValue(sanitized)
	m.themeAccentInput.SetCursor(min(position, len(sanitized)))
}

func sanitizeThemeAccentValue(value string) string {
	var result strings.Builder
	for _, character := range value {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F') {
			result.WriteRune(character)
		}
	}
	return strings.ToUpper(result.String())
}

func (m *Model) reloadSavedAppearance() {
	projectAppearance := m.cfg.UI
	if len(m.configPaths) > 0 {
		loaded, err := config.LoadFiles(m.configPaths)
		if err != nil {
			m.addNotification("appearance", "Could not reload project appearance: "+err.Error(), config.LogError)
			return
		}
		projectAppearance = loaded.UI
	}

	userSettings, err := usersettings.Load(m.settingsPath)
	if err != nil {
		m.addNotification("appearance", "Could not reload global appearance: "+err.Error(), config.LogError)
		return
	}
	name, accent, background, colorMode := effectiveAppearance(projectAppearance, userSettings)
	theme, err := applyAppearance(name, accent, background, colorMode, m.terminalDark)
	if err != nil {
		m.addNotification("appearance", "Could not apply saved appearance: "+err.Error(), config.LogError)
		return
	}

	m.cfg.UI = projectAppearance
	m.userSettings = userSettings
	m.activeTheme = theme
	m.themeBefore = theme
	m.settingsBefore = userSettings
	m.syncThemePickerControls()
	m.addNotification("appearance", "Saved appearance reloaded from configuration", config.LogInfo)
}

func (m *Model) applyThemePicker(names []string) {
	m.updateThemePickerSettings(names)
	m.addNotification("appearance", "Appearance applied for this session: "+m.themePickerSummary(), config.LogInfo)
	m.mode = ModeNormal
}

func (m *Model) saveThemePicker(names []string) {
	m.updateThemePickerSettings(names)
	if err := m.persistSettings(); err != nil {
		m.addNotification("settings", err.Error(), config.LogError)
	} else {
		m.addNotification("appearance", "Appearance saved globally: "+m.themePickerSummary(), config.LogInfo)
	}
	m.mode = ModeNormal
}

func (m *Model) updateThemePickerSettings(names []string) {
	if m.themeUseProject {
		m.userSettings.Theme = ""
	} else {
		m.userSettings.Theme = names[m.themeCursor]
	}
	if m.themeAccentChanged {
		switch {
		case m.themeAccentSource == themeAccentSourceCustom:
			m.userSettings.Accent = m.themeCustomAccent
		case m.themeAccentSource == themeAccentSourceTheme:
			m.userSettings.Accent = "theme"
		case m.themeUseProject:
			// The project owns both the theme and its accent, so no personal
			// override is needed to reproduce this choice.
			m.userSettings.Accent = ""
		default:
			m.userSettings.Accent = strings.TrimSpace(m.cfg.UI.Accent)
		}
	}
	projectBackground := normalizeBackgroundSource(m.cfg.UI.Background)
	if m.themeBackground == projectBackground {
		m.userSettings.Background = ""
	} else {
		m.userSettings.Background = m.themeBackground
	}
	projectColorMode := normalizeColorMode(m.cfg.UI.ColorMode)
	if m.themeColorMode == projectColorMode {
		m.userSettings.ColorMode = ""
	} else {
		m.userSettings.ColorMode = m.themeColorMode
	}
}

func (m *Model) saveThemePickerToProject() {
	path := m.themeProjectConfigPath()
	if path == "" {
		m.addNotification("settings", "No project configuration path is available", config.LogError)
		return
	}
	appearance := config.UIConfig{
		Theme:      m.activeTheme.Name,
		Background: m.themeBackground,
		ColorMode:  m.themeColorMode,
	}
	// An empty resolved accent means the theme keeps its own, which the project
	// file records by leaving the field out rather than pinning a colour.
	appearance.Accent = strings.ToUpper(m.themePickerAccent())
	if err := config.SaveUIAppearance(path, appearance); err != nil {
		m.addNotification("settings", err.Error(), config.LogError)
		return
	}
	// The project file is already authoritative at this point, even if clearing
	// the personal overrides below fails. Keep the in-memory config aligned with
	// disk and then reapply any overrides that could not be removed.
	m.cfg.UI = appearance

	previousSettings := m.userSettings
	m.userSettings.Theme = ""
	m.userSettings.Accent = ""
	m.userSettings.Background = ""
	m.userSettings.ColorMode = ""
	if err := m.persistSettings(); err != nil {
		m.userSettings = previousSettings
		_ = m.applyEffectiveAppearance()
		m.addNotification("settings", "Project appearance was saved, but user overrides could not be cleared: "+err.Error(), config.LogWarn)
	} else {
		m.themeUseProject = true
		// Whatever accent was just written now belongs to the project, so a
		// colour that arrived here as a custom one stops being custom: keeping
		// themeCustomAccent would leave the picker calling the project's own
		// accent "CUSTOM".
		m.themeCustomAccent = ""
		m.themeAccentSource = themeAccentSourceTheme
		if appearance.Accent != "" {
			m.themeAccentSource = themeAccentSourceProject
		}
		m.addNotification("appearance", "Project appearance saved to "+path, config.LogInfo)
	}
	m.configStamps, _ = readConfigStamps(m.configWatchPaths)
	m.mode = ModeNormal
}

// toggleThemeAccentSource switches between the project accent and the theme's
// own. A project without an accent has nothing to toggle, so the key opens the
// editor instead, as the README shortcut table documents.
//
// A consequence worth knowing before "fixing" it: in that project there is no
// scoped way back to the theme default once an accent is committed. Esc closes
// the editor but keeps the committed value; dropping it means cancelling the
// whole picker with Esc or reloading from disk with r. That is accepted rather
// than overlooked — an accent is typed rarely and on purpose, and a key that
// silently discarded it would be the worse trade.
func (m *Model) toggleThemeAccentSource() tea.Cmd {
	if strings.TrimSpace(m.cfg.UI.Accent) == "" {
		return m.beginThemeAccentEdit()
	}
	m.themeAccentChanged = true
	m.themeCustomAccent = ""
	if m.themeAccentSource == themeAccentSourceProject {
		m.themeAccentSource = themeAccentSourceTheme
	} else {
		m.themeAccentSource = themeAccentSourceProject
	}
	m.previewThemePicker()
	return nil
}

func (m *Model) toggleThemeBackgroundSource() {
	if m.themeBackground == backgroundTerminal {
		m.themeBackground = backgroundTheme
	} else {
		m.themeBackground = backgroundTerminal
	}
	m.previewThemePicker()
}

func (m *Model) cycleThemeColorMode() {
	switch m.themeColorMode {
	case colorModeAuto:
		m.themeColorMode = colorModeDark
	case colorModeDark:
		m.themeColorMode = colorModeLight
	default:
		m.themeColorMode = colorModeAuto
	}
	m.previewThemePicker()
}

func (m *Model) cancelThemePicker() {
	m.themeAccentInput.Blur()
	m.themeAccentEditing = false
	m.userSettings = m.settingsBefore
	m.activeTheme = m.themeBefore
	applyPalette(m.themeBefore)
	m.mode = ModeNormal
}

func (m *Model) previewThemePicker() {
	name := ThemeNames()[m.themeCursor]
	if m.themeUseProject {
		name = m.cfg.UI.Theme
		if name == "" {
			name = DefaultTheme
		}
	}
	theme, err := applyAppearance(name, m.themePickerAccent(), m.themeBackground, m.themeColorMode, m.terminalDark)
	if err == nil {
		m.activeTheme = theme
	}
}

func (m *Model) applyEffectiveAppearance() error {
	name, accent, background, colorMode := effectiveAppearance(m.cfg.UI, m.userSettings)
	theme, err := applyAppearance(name, accent, background, colorMode, m.terminalDark)
	if err != nil {
		return err
	}
	m.activeTheme = theme
	return nil
}

func (m *Model) themeProjectConfigPath() string {
	if len(m.configPaths) == 0 {
		return ""
	}
	return m.configPaths[len(m.configPaths)-1]
}

// themePickerAccent resolves the accent the picker currently represents. An
// empty result means the selected theme keeps its own accent. Preview, label,
// and both save paths read the answer from here so the source cannot be
// interpreted differently in each of them.
func (m *Model) themePickerAccent() string {
	switch m.themeAccentSource {
	case themeAccentSourceCustom:
		return m.themeCustomAccent
	case themeAccentSourceProject:
		return strings.TrimSpace(m.cfg.UI.Accent)
	default:
		return ""
	}
}

func isCustomAccent(accent, projectAccent string) bool {
	accent = strings.TrimSpace(accent)
	return accent != "" && accent != "auto" && accent != "theme" && !strings.EqualFold(accent, strings.TrimSpace(projectAccent))
}

// themePickerSummary describes the applied appearance for the confirmation
// notification. It reuses the picker's own label helpers on purpose: a second
// set of conditions is how the two drifted apart before, when the panel read
// themeCustomAccent and this summary still only knew Theme versus Project.
func (m *Model) themePickerSummary() string {
	projectTheme := m.cfg.UI.Theme
	if projectTheme == "" {
		projectTheme = DefaultTheme
	}
	return strings.Join([]string{
		"Theme " + m.themePickerThemeLabel(projectTheme),
		"Accent " + m.themePickerAccentLabel(),
		"Background " + m.themePickerBackgroundLabel(),
		"Mode " + m.themePickerColorModeLabel(),
	}, " / ")
}

func (m *Model) persistSettings() error {
	if m.settingsPath == "" {
		return nil
	}
	return usersettings.Save(m.settingsPath, m.userSettings)
}
