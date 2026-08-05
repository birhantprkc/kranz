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
	m.themeOriginalAccent = m.userSettings.Accent
	m.themeAccentChanged = false
	m.themeCustomAccent = ""
	if isCustomAccent(m.userSettings.Accent, m.cfg.UI.Accent) {
		m.themeCustomAccent = strings.ToUpper(strings.TrimSpace(m.userSettings.Accent))
	}
	m.themeProjectAccent = projectAccent != "" && m.userSettings.Accent != "theme" &&
		(m.themeUseProject || strings.EqualFold(m.userSettings.Accent, projectAccent))
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
		m.themeProjectAccent = false
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
		case m.themeCustomAccent != "":
			m.userSettings.Accent = m.themeCustomAccent
		case !m.themeProjectAccent:
			m.userSettings.Accent = "theme"
		case m.themeUseProject:
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
	if m.themeCustomAccent != "" {
		appearance.Accent = m.themeCustomAccent
	} else if m.themeProjectAccent || (!m.themeAccentChanged && isCustomAccent(m.themeOriginalAccent, m.cfg.UI.Accent)) {
		appearance.Accent = m.activeTheme.Accent
	}
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
		m.themeProjectAccent = appearance.Accent != ""
		m.themeOriginalAccent = ""
		m.addNotification("appearance", "Project appearance saved to "+path, config.LogInfo)
	}
	m.configStamps, _ = readConfigStamps(m.configWatchPaths)
	m.mode = ModeNormal
}

func (m *Model) toggleThemeAccentSource() tea.Cmd {
	if strings.TrimSpace(m.cfg.UI.Accent) == "" {
		return m.beginThemeAccentEdit()
	}
	m.themeAccentChanged = true
	m.themeCustomAccent = ""
	m.themeProjectAccent = !m.themeProjectAccent
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
	accent := ""
	if m.themeCustomAccent != "" {
		accent = m.themeCustomAccent
	} else if !m.themeAccentChanged && isCustomAccent(m.themeOriginalAccent, m.cfg.UI.Accent) {
		accent = m.themeOriginalAccent
	} else if m.themeProjectAccent {
		accent = strings.TrimSpace(m.cfg.UI.Accent)
	}
	theme, err := applyAppearance(name, accent, m.themeBackground, m.themeColorMode, m.terminalDark)
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

func isCustomAccent(accent, projectAccent string) bool {
	accent = strings.TrimSpace(accent)
	return accent != "" && accent != "auto" && accent != "theme" && !strings.EqualFold(accent, strings.TrimSpace(projectAccent))
}

func (m *Model) themePickerSummary() string {
	theme := "Selected · " + ThemeNames()[m.themeCursor]
	if m.themeUseProject {
		theme = "Project · " + m.activeTheme.Name
	}
	accent := "Theme"
	if m.themeProjectAccent {
		accent = "Project"
	}
	background := "Terminal"
	if m.themeBackground == backgroundTheme {
		background = "Theme"
	}
	return theme + " / " + accent + " accent / " + background + " background / " + strings.ToUpper(m.themeColorMode)
}

func (m *Model) persistSettings() error {
	if m.settingsPath == "" {
		return nil
	}
	return usersettings.Save(m.settingsPath, m.userSettings)
}
