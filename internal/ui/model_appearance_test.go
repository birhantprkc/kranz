package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/kranz-org/kranz/internal/config"
	usersettings "github.com/kranz-org/kranz/internal/settings"
	"github.com/muesli/termenv"
)

// Tests for appearance resolution and the theme picker.

func TestThemeOverridePrecedenceAndPersistence(t *testing.T) {
	defer func() { _, _ = ApplyTheme(DefaultTheme, "") }()
	settingsPath := filepath.Join(t.TempDir(), "settings.yaml")
	model := NewModelWithOptions(&config.Config{
		Project: "Theme", UI: config.UIConfig{Theme: "nord", Accent: "#88C0D0"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test", ModelOptions{
		Settings: usersettings.Settings{Theme: "dracula", Accent: "#FF00FF"}, SettingsPath: settingsPath,
	})
	defer model.Shutdown()
	if model.activeTheme.Name != "dracula" || model.activeTheme.Accent != "#FF00FF" {
		t.Fatalf("resolved theme = %#v", model.activeTheme)
	}

	model.openThemePicker()
	model.themeCursor = 0
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	saved, err := usersettings.Load(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Theme != "kranz" || saved.Accent != "#FF00FF" {
		t.Fatalf("saved override = %#v", saved)
	}
}

func TestThemePickerEnterAppliesWithoutSaving(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.yaml")
	model := NewModelWithOptions(&config.Config{
		Project: "Session", UI: config.UIConfig{Theme: "forest"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test", ModelOptions{SettingsPath: settingsPath})
	defer model.Shutdown()

	model.openThemePicker()
	pressKey(model, 'j')
	wantTheme := model.activeTheme.Name
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyEnter})

	if model.mode != ModeNormal || model.userSettings.Theme != wantTheme || model.activeTheme.Name != wantTheme {
		t.Fatalf("session appearance = mode %v, active %q, settings %#v", model.mode, model.activeTheme.Name, model.userSettings)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("Enter wrote global settings: %v", err)
	}

	model.openThemePicker()
	if model.themeUseProject || ThemeNames()[model.themeCursor] != wantTheme {
		t.Fatalf("reopened picker lost session theme: project=%v cursor=%q", model.themeUseProject, ThemeNames()[model.themeCursor])
	}
}

func TestThemeAccentEditorOpensWithoutProjectAccentAndAppliesHexSuffix(t *testing.T) {
	model := NewModel(&config.Config{
		Project: "Custom Accent", UI: config.UIConfig{Theme: "forest"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test")
	defer model.Shutdown()
	model.width, model.height, model.ready = 110, 32, true
	model.openThemePicker()
	originalAccent := model.activeTheme.Accent

	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !model.themeAccentEditing || model.themeAccentInput.Value() != strings.TrimPrefix(originalAccent, "#") {
		t.Fatalf("accent editor = active %v / value %q", model.themeAccentEditing, model.themeAccentInput.Value())
	}
	plain := ansi.Strip(model.renderThemeView())
	for _, expected := range []string{"Accent: #" + strings.TrimPrefix(originalAccent, "#"), "[Enter] Apply", "[Esc] Cancel", "[a/Shift+A] Accent: Edit color"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("accent editor does not contain %q:\n%s", expected, plain)
		}
	}

	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("#12abefz")})
	if got := model.themeAccentInput.Value(); got != "12ABEF" {
		t.Fatalf("sanitized accent input = %q", got)
	}
	candidateStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#12ABEF")).Bold(true)
	accentSetting := model.renderThemePickerAccentSetting()
	if !strings.Contains(accentSetting, candidateStyle.Render("12ABEF")) || !strings.Contains(accentSetting, candidateStyle.Render("●")) {
		t.Fatalf("valid accent candidate is not highlighted: %q", accentSetting)
	}
	if model.activeTheme.Accent != originalAccent {
		t.Fatalf("accent changed before Enter: %q", model.activeTheme.Accent)
	}
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if model.themeAccentEditing || model.mode != ModeThemes || model.activeTheme.Accent != "#12ABEF" || model.themeCustomAccent != "#12ABEF" {
		t.Fatalf("applied accent editor = editing %v / mode %v / accent %q / custom %q",
			model.themeAccentEditing, model.mode, model.activeTheme.Accent, model.themeCustomAccent)
	}

	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if model.mode != ModeNormal || model.userSettings.Accent != "#12ABEF" {
		t.Fatalf("session accent = mode %v / settings %#v", model.mode, model.userSettings)
	}
}

func TestThemeAccentEditorRejectsIncompleteInputAndEscCancels(t *testing.T) {
	model := NewModel(&config.Config{
		Project: "Project Accent", UI: config.UIConfig{Theme: "forest", Accent: "#2AB630"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test")
	defer model.Shutdown()
	model.width, model.height, model.ready = 110, 32, true
	model.openThemePicker()
	originalAccent := model.activeTheme.Accent

	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	initialSetting := ansi.Strip(model.renderThemePickerAccentSetting())
	initialActionColumn := strings.Index(initialSetting, "[Enter]")
	if initialActionColumn < 0 {
		t.Fatalf("accent editor actions are missing: %q", initialSetting)
	}
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("123")})
	partialSetting := ansi.Strip(model.renderThemePickerAccentSetting())
	if actionColumn := strings.Index(partialSetting, "[Enter]"); actionColumn != initialActionColumn {
		t.Fatalf("accent editor actions shifted from column %d to %d: initial %q / partial %q",
			initialActionColumn, actionColumn, initialSetting, partialSetting)
	}
	if !strings.Contains(partialSetting, "○") {
		t.Fatalf("incomplete accent does not reserve the swatch: %q", partialSetting)
	}
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !model.themeAccentEditing || model.themeAccentError == "" || model.activeTheme.Accent != originalAccent {
		t.Fatalf("incomplete accent = editing %v / error %q / accent %q", model.themeAccentEditing, model.themeAccentError, model.activeTheme.Accent)
	}
	if plain := ansi.Strip(model.renderThemeView()); !strings.Contains(plain, "Enter 6 hex digits") {
		t.Fatalf("accent validation is not visible:\n%s", plain)
	}

	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if model.themeAccentEditing || model.mode != ModeThemes || model.activeTheme.Accent != originalAccent || model.themeAccentChanged {
		t.Fatalf("cancelled accent editor = editing %v / mode %v / accent %q / changed %v",
			model.themeAccentEditing, model.mode, model.activeTheme.Accent, model.themeAccentChanged)
	}
}

func TestThemePickerReloadsSavedAppearanceFromDisk(t *testing.T) {
	tempDir := t.TempDir()
	projectPath := filepath.Join(tempDir, "kranz.yaml")
	projectData := "project: Reloaded\nui:\n  theme: forest\n  accent: '#2AB630'\nservices:\n  app:\n    command: exit 0\n"
	if err := os.WriteFile(projectPath, []byte(projectData), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(tempDir, "settings.yaml")
	savedSettings := usersettings.Settings{Theme: "dracula", Background: backgroundTheme, ColorMode: colorModeDark}
	if err := usersettings.Save(settingsPath, savedSettings); err != nil {
		t.Fatal(err)
	}

	model := NewModelWithOptions(&config.Config{
		Project: "Stale", UI: config.UIConfig{Theme: "nord"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test", ModelOptions{
		Settings:     usersettings.Settings{Theme: "github-light"},
		SettingsPath: settingsPath,
		ConfigPaths:  []string{projectPath},
	})
	defer model.Shutdown()
	model.openThemePicker()
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	if model.mode != ModeThemes || model.cfg.UI.Theme != "forest" || model.userSettings != savedSettings || model.activeTheme.Name != "dracula" {
		t.Fatalf("reloaded appearance = mode %v / project %#v / settings %#v / active %q",
			model.mode, model.cfg.UI, model.userSettings, model.activeTheme.Name)
	}
	if model.themeUseProject || ThemeNames()[model.themeCursor] != "dracula" || model.themeBackground != backgroundTheme || model.themeColorMode != colorModeDark {
		t.Fatalf("reloaded picker controls = project %v / theme %q / background %q / mode %q",
			model.themeUseProject, ThemeNames()[model.themeCursor], model.themeBackground, model.themeColorMode)
	}
	if model.activeTheme.TerminalCanvas {
		t.Fatal("saved theme background ownership was not restored")
	}
	model.cancelThemePicker()
	if model.activeTheme.Name != "dracula" || model.userSettings != savedSettings {
		t.Fatalf("cancel reverted the reloaded baseline: %q / %#v", model.activeTheme.Name, model.userSettings)
	}
}

func TestUserThemeUsesItsOwnAccentInsteadOfProjectAccent(t *testing.T) {
	themeName, accent, background, colorMode := effectiveAppearance(
		config.UIConfig{Theme: "ocean", Accent: "#31C5F4"},
		usersettings.Settings{Theme: "dracula"},
	)
	if themeName != "dracula" || accent != "" || background != backgroundTerminal || colorMode != colorModeAuto {
		t.Fatalf("user theme appearance = %q/%q/%q/%q", themeName, accent, background, colorMode)
	}
	theme, err := ApplyTheme(themeName, accent)
	if err != nil {
		t.Fatal(err)
	}
	if theme.Accent != "#BD93F9" {
		t.Fatalf("Dracula accent = %q, want #BD93F9", theme.Accent)
	}
}

func TestThemePickerAAppliesProjectAccentToSelectedTheme(t *testing.T) {
	defer func() { _, _ = ApplyTheme(DefaultTheme, "") }()
	model := NewModelWithOptions(&config.Config{
		Project: "Branded", UI: config.UIConfig{Theme: "forest", Accent: "#2AB630"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test", ModelOptions{Settings: usersettings.Settings{Theme: "dracula"}})
	defer model.Shutdown()
	model.openThemePicker()
	for index, name := range ThemeNames() {
		if name == "github-light" {
			model.themeCursor = index
			break
		}
	}
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !model.themeProjectAccent || model.activeTheme.Accent != "#2AB630" {
		t.Fatalf("project accent was not previewed: toggle=%v theme=%q", model.themeProjectAccent, model.activeTheme.Accent)
	}
	if model.activeTheme.Name != "github-light" {
		t.Fatalf("selected theme changed to %q", model.activeTheme.Name)
	}
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if model.mode != ModeNormal || model.activeTheme.Accent != "#2AB630" || model.userSettings.Accent != "#2AB630" || model.userSettings.Theme != "github-light" {
		t.Fatalf("applied picker state = mode %v, active %#v, settings %#v", model.mode, model.activeTheme, model.userSettings)
	}
}

func TestThemePickerUsesClearProjectAndAccentToggles(t *testing.T) {
	model := NewModel(&config.Config{
		Project: "Branded", UI: config.UIConfig{Theme: "forest", Accent: "#2AB630"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test")
	defer model.Shutdown()
	model.width, model.height, model.ready = 110, 32, true
	model.openThemePicker()
	if !model.themeUseProject || !model.themeProjectAccent {
		t.Fatalf("initial picker modes = project %v accent %v", model.themeUseProject, model.themeProjectAccent)
	}
	pressKey(model, 'p')
	if model.themeUseProject {
		t.Fatal("p did not switch to selected theme")
	}
	pressKey(model, 'p')
	if !model.themeUseProject {
		t.Fatal("p did not switch back to project theme")
	}
	pressKey(model, 'a')
	if model.themeProjectAccent {
		t.Fatal("a did not switch to the theme accent")
	}
	plain := ansi.Strip(model.renderThemeView())
	for _, expected := range []string{
		"Theme: PROJECT · forest", "Accent: THEME DEFAULT", "Background: TERMINAL · inherited", "Mode: AUTO · Dark detected",
		"[p] Theme: Project / Selected", "[a] Accent: Project / Theme default", "[b] Background: Terminal / Theme",
		"[m] Mode: Auto / Dark / Light", "SESSION", "[Enter] Apply", "[r] Reload saved", "SAVE", "[g] Global", "[c] Project",
	} {
		if !strings.Contains(plain, expected) {
			t.Errorf("theme picker does not explain %q:\n%s", expected, plain)
		}
	}
}

func TestBackgroundSourceIsIndependentAndUserOverrideWins(t *testing.T) {
	defer func() { _, _ = ApplyTheme(DefaultTheme, "") }()
	darkTerminal := true
	project := config.UIConfig{Theme: "github-light", Accent: "#0969DA", Background: "theme", ColorMode: "dark"}
	model := NewModelWithOptions(&config.Config{
		Project: "Exact", UI: project,
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test", ModelOptions{DarkBackground: &darkTerminal})
	defer model.Shutdown()
	if relativeLuminance(mustParseColor(t, model.activeTheme.Background)) >= 0.2 || model.activeTheme.TerminalCanvas {
		t.Fatalf("project dark painted background = %#v", model.activeTheme)
	}

	model.userSettings.Background = backgroundTerminal
	if err := model.applyEffectiveAppearance(); err != nil {
		t.Fatal(err)
	}
	if relativeLuminance(mustParseColor(t, model.activeTheme.Background)) >= 0.2 {
		t.Fatalf("terminal override did not produce a dark canvas: %s", model.activeTheme.Background)
	}
	if model.activeTheme.Accent != "#0969DA" {
		t.Fatalf("background override changed the independent accent: %s", model.activeTheme.Accent)
	}
}

func TestThemePickerPersistsBackgroundOverrideAgainstProject(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.yaml")
	model := NewModelWithOptions(&config.Config{
		Project: "Exact", UI: config.UIConfig{Theme: "forest", Background: "theme"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test", ModelOptions{SettingsPath: settingsPath})
	defer model.Shutdown()
	model.openThemePicker()
	if model.themeBackground != backgroundTheme {
		t.Fatalf("picker background = %q, want project theme source", model.themeBackground)
	}
	pressKey(model, 'b')
	if model.themeBackground != backgroundTerminal {
		t.Fatalf("b background = %q, want terminal", model.themeBackground)
	}
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	saved, err := usersettings.Load(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Background != backgroundTerminal {
		t.Fatalf("saved background override = %#v", saved)
	}
}

func TestPaintedCreamThemeSupportsAutoDarkAndForcedLight(t *testing.T) {
	defer func() { _, _ = ApplyTheme(DefaultTheme, "") }()
	darkTerminal := true
	model := NewModelWithOptions(&config.Config{
		Project: "Cream", UI: config.UIConfig{Theme: "cream", Background: "theme", ColorMode: "auto"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test", ModelOptions{DarkBackground: &darkTerminal})
	defer model.Shutdown()
	if model.activeTheme.TerminalCanvas || relativeLuminance(mustParseColor(t, model.activeTheme.Background)) >= 0.2 {
		t.Fatalf("automatic cream dark variant = %#v", model.activeTheme)
	}

	model.openThemePicker()
	pressKey(model, 'm') // auto -> dark
	pressKey(model, 'm') // dark -> light
	if model.themeColorMode != colorModeLight || relativeLuminance(mustParseColor(t, model.activeTheme.Background)) < 0.7 {
		t.Fatalf("forced cream light variant = %q/%#v", model.themeColorMode, model.activeTheme)
	}
	pressKey(model, 'm') // light -> auto
	if model.themeColorMode != colorModeAuto || relativeLuminance(mustParseColor(t, model.activeTheme.Background)) >= 0.2 {
		t.Fatalf("cream auto cycle = %q/%#v", model.themeColorMode, model.activeTheme)
	}
}

func TestGlobalColorModeOverridePersistsIndependently(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.yaml")
	darkTerminal := true
	model := NewModelWithOptions(&config.Config{
		Project: "Mode", UI: config.UIConfig{Theme: "cream", Background: "theme", ColorMode: "dark"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test", ModelOptions{
		Settings:       usersettings.Settings{ColorMode: "light"},
		SettingsPath:   settingsPath,
		DarkBackground: &darkTerminal,
	})
	defer model.Shutdown()
	if relativeLuminance(mustParseColor(t, model.activeTheme.Background)) < 0.7 {
		t.Fatalf("global light override was not applied: %#v", model.activeTheme)
	}
	model.openThemePicker()
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	saved, err := usersettings.Load(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ColorMode != colorModeLight {
		t.Fatalf("global color mode = %#v", saved)
	}
}

func TestThemePickerKeepsAllControlsVisibleAtTwentyFourRows(t *testing.T) {
	model := NewModel(&config.Config{
		Project: "Compact", UI: config.UIConfig{Theme: "forest", Accent: "#2AB630"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test")
	defer model.Shutdown()
	model.width, model.height, model.ready = 80, 24, true
	model.settingsPath = "/tmp/settings.yaml"
	model.configPaths = []string{"/tmp/kranz.yaml"}
	model.openThemePicker()

	plain := ansi.Strip(model.renderThemeView())
	for _, expected := range []string{"Preview", "Accent background", "Neutral background", "[p] Theme: Project / Selected", "[a] Accent: Project / Theme default", "[b] Background: Terminal / Theme", "[m] Mode: Auto / Dark / Light", "SESSION", "[Enter] Apply", "[r] Reload saved", "[Esc] Cancel", "SAVE", "[g] Global", "[c] Project", "Global: /tmp/settings.yaml", "Project: /tmp/kranz.yaml"} {
		if !strings.Contains(plain, expected) {
			t.Errorf("24-row theme picker clipped %q:\n%s", expected, plain)
		}
	}
	plainLines := strings.Split(plain, "\n")
	themePositionLine, controlsLine := -1, -1
	escapeLine, globalLine := -1, -1
	for index, line := range plainLines {
		if strings.Contains(line, "14/19") {
			themePositionLine = index
		}
		if strings.Contains(line, "[p] Theme: Project / Selected") {
			controlsLine = index
		}
		if strings.Contains(line, "[Esc] Cancel") {
			escapeLine = index
		}
		if strings.Contains(line, "Global: /tmp/settings.yaml") {
			globalLine = index
		}
	}
	if escapeLine < 0 || globalLine != escapeLine+3 {
		t.Errorf("config paths are not separated from the controls:\n%s", plain)
	}
	if themePositionLine < 0 || controlsLine < themePositionLine+2 {
		t.Errorf("theme list is not separated from the controls:\n%s", plain)
	}
}

func TestMouseControlsCompleteThemePicker(t *testing.T) {
	model := NewModel(&config.Config{
		Project: "Branded", UI: config.UIConfig{Theme: "forest", Accent: "#2AB630"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test")
	defer model.Shutdown()
	model.width, model.height, model.ready = 110, 32, true
	model.openThemePicker()
	clickRenderedText(t, model, "Accent: PROJECT · #2AB630")
	if !model.themeAccentEditing {
		t.Fatal("clicking the accent field did not open its editor")
	}
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if model.themeAccentEditing || model.mode != ModeThemes {
		t.Fatalf("accent field Esc left editing/mode %v/%v", model.themeAccentEditing, model.mode)
	}

	clickRenderedText(t, model, "GitHub Light")
	if model.themeUseProject || model.activeTheme.Name != "github-light" {
		t.Fatalf("theme row click = project %v / %s", model.themeUseProject, model.activeTheme.Name)
	}
	clickRenderedText(t, model, "[p] Theme: Project / Selected")
	if !model.themeUseProject || model.activeTheme.Name != "forest" {
		t.Fatalf("project toggle click = project %v / %s", model.themeUseProject, model.activeTheme.Name)
	}
	clickRenderedText(t, model, "[a] Accent: Project / Theme default")
	if model.themeProjectAccent {
		t.Fatal("accent toggle click did not select the theme default")
	}
	clickRenderedText(t, model, "[b] Background: Terminal / Theme")
	if model.themeBackground != backgroundTheme {
		t.Fatal("background toggle click did not select a painted theme background")
	}
	clickRenderedText(t, model, "[m] Mode: Auto / Dark / Light")
	if model.themeColorMode != colorModeDark {
		t.Fatal("mode toggle click did not select the dark variant")
	}
	clickRenderedText(t, model, "[Enter] Apply")
	if model.mode != ModeNormal || model.userSettings.Theme != "" || model.userSettings.Accent != "theme" || model.userSettings.Background != "theme" || model.userSettings.ColorMode != "dark" {
		t.Fatalf("theme apply click left mode/settings %v/%#v", model.mode, model.userSettings)
	}

	model.openThemePicker()
	clickRenderedText(t, model, "[r] Reload saved")
	if model.mode != ModeThemes || model.activeTheme.Name != "forest" || model.userSettings != (usersettings.Settings{}) {
		t.Fatalf("reload saved click left mode/theme/settings %v/%q/%#v", model.mode, model.activeTheme.Name, model.userSettings)
	}

	clickRenderedText(t, model, "[g] Global")
	if model.mode != ModeNormal {
		t.Fatalf("global theme save click left mode %v", model.mode)
	}
}

func TestThemePickerRowIsClickableOutsideItsName(t *testing.T) {
	model := NewModel(&config.Config{
		Project: "Clickable", UI: config.UIConfig{Theme: "forest", Accent: "#2AB630"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test")
	defer model.Shutdown()
	model.width, model.height, model.ready = 110, 32, true
	model.openThemePicker()

	rendered := model.View()
	for y, line := range strings.Split(ansi.Strip(rendered), "\n") {
		nameStart := strings.Index(line, "GitHub Light")
		if nameStart < 0 {
			continue
		}
		// Click the palette at the end of the row, not the theme name.
		theme, ok := LookupTheme("github-light")
		if !ok {
			t.Fatal("github-light theme not found")
		}
		x := lipgloss.Width(line[:nameStart]) + 20 + lipgloss.Width(themePalettePreview(theme)) - 1
		_, _ = model.handleMouseMsg(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		if model.themeUseProject || model.activeTheme.Name != "github-light" {
			t.Fatalf("palette click = project %v / %s", model.themeUseProject, model.activeTheme.Name)
		}
		return
	}
	t.Fatalf("GitHub Light row not found:\n%s", ansi.Strip(rendered))
}

func TestThemePickerUsesThemeIdentityColorsEvenWithProjectAccent(t *testing.T) {
	model := NewModel(&config.Config{
		Project: "Branded", UI: config.UIConfig{Theme: "forest", Accent: "#2AB630"},
		Services: map[string]config.Service{"app": {Command: "exit 0"}},
	}, "test")
	defer model.Shutdown()
	model.width, model.height, model.ready = 110, 32, true
	model.openThemePicker()
	model.themeUseProject = false
	model.themeProjectAccent = true

	theme, ok := LookupTheme("dracula")
	if !ok {
		t.Fatal("dracula theme not found")
	}
	for index, name := range ThemeNames() {
		if name == theme.Name {
			model.themeCursor = index
			break
		}
	}
	model.previewThemePicker()
	wantName := themeNameStyle(theme).Render(theme.DisplayName)
	if setting := model.renderThemePickerThemeSetting(model.cfg.UI.Theme); !strings.Contains(setting, wantName) {
		t.Fatalf("theme setting does not use Dracula identity color: %q", setting)
	}

	preview := themePalettePreview(theme)
	plainPreview := ansi.Strip(preview)
	if plainPreview != "● ● ● ●" {
		t.Fatalf("palette preview = %q", plainPreview)
	}
	card := renderThemePreviewCard(theme)
	plainCard := ansi.Strip(card)
	for _, label := range []string{"Preview", "Text", "Muted text", "Accent background", "Neutral background"} {
		if !strings.Contains(plainCard, label) {
			t.Fatalf("theme preview card does not contain %q: %q", label, plainCard)
		}
	}
	if lipgloss.Height(card) != 6 || !strings.Contains(plainCard, "╭") || !strings.Contains(plainCard, "╰") {
		t.Fatalf("theme preview is not a bordered service-like card: %q", plainCard)
	}
	cardLines := strings.Split(plainCard, "\n")
	if strings.Contains(cardLines[1], "─") || strings.Count(cardLines[1], "│") != 2 {
		t.Fatalf("theme preview contains nested border artifacts: %q", plainCard)
	}

	accentSetting := model.renderThemePickerAccentSetting()
	wantAccent := lipgloss.NewStyle().Foreground(lipgloss.Color(model.cfg.UI.Accent)).Bold(true).Render(model.cfg.UI.Accent)
	if !strings.Contains(accentSetting, wantAccent) {
		t.Fatalf("accent setting does not color its hex value: %q", accentSetting)
	}
}

func TestThemePathsWrapWithoutEllipsis(t *testing.T) {
	path := "/a/very/long/project/directory/whose/config/name-must-remain-visible/kranz.yaml"
	lines := renderThemePath("Project", path, 24)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if strings.Contains(plain, "…") {
		t.Fatalf("wrapped path was truncated: %q", plain)
	}
	compact := strings.Join(strings.Fields(plain), "")
	if compact != "Project:"+path {
		t.Fatalf("wrapped path lost content: %q", plain)
	}
}

func TestThemePickerSavesAppearanceToProjectConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kranz.yaml")
	data := "project: Project Theme\nui:\n  theme: forest\n  accent: '#2AB630'\n  background: terminal\nservices:\n  app:\n    command: exit 0\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(t.TempDir(), "settings.yaml")
	model := NewModelWithOptions(cfg, "test", ModelOptions{
		Settings:     usersettings.Settings{Theme: "dracula", Accent: "theme", Background: "theme"},
		SettingsPath: settingsPath,
	})
	defer model.Shutdown()
	model.openThemePicker()
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if model.mode != ModeNormal {
		t.Fatal("successful project save did not close the theme picker")
	}

	savedConfig, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := config.UIConfig{Theme: "dracula", Background: "theme", ColorMode: "auto"}
	if savedConfig.UI != want {
		t.Fatalf("project appearance = %#v, want %#v", savedConfig.UI, want)
	}
	savedSettings, err := usersettings.Load(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if savedSettings != (usersettings.Settings{}) {
		t.Fatalf("user overrides were not cleared: %#v", savedSettings)
	}
}

func TestThemeAccentEditorSavesCustomColorToProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kranz.yaml")
	data := "project: Custom Accent\nui:\n  theme: forest\n  accent: '#2AB630'\nservices:\n  app:\n    command: exit 0\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	model := NewModel(cfg, "test")
	defer model.Shutdown()
	model.openThemePicker()
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("#445566")})
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = model.handleThemeKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})

	saved, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.UI.Accent != "#445566" || model.mode != ModeNormal {
		t.Fatalf("saved custom accent = %q / mode %v", saved.UI.Accent, model.mode)
	}
}

func TestLightTerminalUsesCohesiveAdaptiveCanvas(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(previousProfile)
	defer func() { _, _ = ApplyTheme(DefaultTheme, "") }()

	darkBackground := false
	model := NewModelWithOptions(&config.Config{
		Project: "MyClass", UI: config.UIConfig{Theme: "forest", Accent: "#2AB630"},
		Services: map[string]config.Service{"im-core": {Command: "npm run dev", Description: "Messenger API"}},
	}, "test", ModelOptions{DarkBackground: &darkBackground})
	defer model.Shutdown()
	model.width, model.height, model.ready = 80, 24, true

	model.openThemePicker()
	if model.terminalDark || model.themePickerBackgroundLabel() != "TERMINAL · inherited" || model.themePickerColorModeLabel() != "AUTO · Light detected" {
		t.Fatalf("terminal mode = %v/%q/%q", model.terminalDark, model.themePickerBackgroundLabel(), model.themePickerColorModeLabel())
	}
	model.cancelThemePicker()
	if relativeLuminance(mustParseColor(t, model.activeTheme.Background)) < 0.7 {
		t.Fatalf("canvas did not adapt to light terminal: %#v", model.activeTheme)
	}
	if model.activeTheme.Background != model.activeTheme.Surface {
		t.Fatalf("adaptive canvas/panel split = %s/%s", model.activeTheme.Background, model.activeTheme.Surface)
	}
	_, appUsesTerminal := AppStyle.GetBackground().(lipgloss.NoColor)
	_, panelUsesTerminal := PanelStyle.GetBackground().(lipgloss.NoColor)
	if !model.activeTheme.TerminalCanvas || !appUsesTerminal || !panelUsesTerminal {
		t.Fatalf("terminal-owned canvas is still painted: theme=%v app=%#v panel=%#v",
			model.activeTheme.TerminalCanvas, AppStyle.GetBackground(), PanelStyle.GetBackground())
	}
	rendered := model.View()
	if height := lipgloss.Height(rendered); height != model.height {
		t.Fatalf("adaptive view height = %d, want %d", height, model.height)
	}
}

func TestExactThemeNestedStylesRestoreTheCanvasBackground(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(previousProfile)
	defer func() { _, _ = ApplyTheme(DefaultTheme, "") }()

	for _, testCase := range []struct {
		name         string
		theme        string
		darkTerminal bool
	}{
		{name: "dark ocean", theme: "ocean", darkTerminal: true},
		{name: "light GitHub", theme: "github-light", darkTerminal: true},
		{name: "warm cream", theme: "cream", darkTerminal: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model := NewModelWithOptions(&config.Config{
				Project: "Uniform", UI: config.UIConfig{Theme: testCase.theme, Background: backgroundTheme},
				Services: map[string]config.Service{"api": {Command: "exit 0", Tags: []string{"backend"}}},
			}, "test", ModelOptions{DarkBackground: &testCase.darkTerminal})
			defer model.Shutdown()
			model.width, model.height, model.ready = 100, 28, true

			assertFrameRestoresCanvasBackground(t, model.View())
			model.openThemePicker()
			assertFrameRestoresCanvasBackground(t, model.View())
		})
	}
}

func assertFrameRestoresCanvasBackground(t *testing.T, frame string) {
	t.Helper()
	backgroundPrefix := terminalStylePrefix(lipgloss.NewStyle().Background(ColorBackground))
	if backgroundPrefix == "" {
		t.Fatal("true-color background style did not produce an ANSI prefix")
	}
	const reset = "\x1b[0m"
	if !strings.HasSuffix(frame, reset) {
		t.Fatal("frame does not end by resetting terminal styles")
	}
	for offset := 0; ; {
		relative := strings.Index(frame[offset:], reset)
		if relative < 0 {
			break
		}
		resetEnd := offset + relative + len(reset)
		if resetEnd < len(frame) && frame[resetEnd] != '\n' && !strings.HasPrefix(frame[resetEnd:], backgroundPrefix) {
			t.Fatalf("nested reset at byte %d exposes terminal background; next bytes %q", resetEnd-len(reset), frame[resetEnd:min(len(frame), resetEnd+len(backgroundPrefix)+12)])
		}
		offset = resetEnd
	}
}
