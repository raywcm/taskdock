package config

import "testing"

func TestLanguageDetectionAndOverrides(t *testing.T) {
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANGUAGE", "")
	t.Setenv("LANG", "en_US.UTF-8")
	if Language("auto") != "zh-CN" || Language("en") != "en" {
		t.Fatal("locale priority or override failed")
	}
	t.Setenv("LC_ALL", "")
	if Language("auto") != "en" {
		t.Fatal("English fallback failed")
	}
}

func TestAllLanguagesValidateAndDetectRegionalLocales(t *testing.T) {
	for _, option := range Languages() {
		s := Defaults()
		s.Language = option.Code
		if err := s.Validate(); err != nil {
			t.Fatalf("%s rejected: %v", option.Code, err)
		}
		if LanguageName(option.Code) == "" {
			t.Fatal("missing native language name")
		}
	}
	for input, want := range map[string]string{"ja_JP.UTF-8": "ja", "es_MX.UTF-8": "es", "fr_CA.UTF-8": "fr", "en_GB.UTF-8": "en", "zh_CN.UTF-8": "zh-CN", "it:fr:en": "fr", "C.UTF-8": "en", "it_IT.UTF-8": "en"} {
		t.Setenv("LC_ALL", input)
		if got := Language("auto"); got != want {
			t.Fatalf("%s detected as %s, want %s", input, got, want)
		}
	}
	s := Defaults()
	s.Language = "unknown"
	if s.Validate() == nil {
		t.Fatal("unsupported persisted language accepted")
	}
}

func TestHistoryRetentionDefaultsAndDurations(t *testing.T) {
	if Defaults().HistoryRetentionSeconds != 3600 {
		t.Fatal("default must be one hour")
	}
	for value, want := range map[string]int64{"off": 0, "0": 0, "30m": 1800, "1h": 3600, "24h": 86400, "1s": 1} {
		got, err := With(Defaults(), "history-retention", value)
		if err != nil || got.HistoryRetentionSeconds != want {
			t.Fatal(value, got, err)
		}
	}
	for _, value := range []string{"-1h", "0.5s", "10000h", "invalid"} {
		if _, err := With(Defaults(), "history-retention", value); err == nil {
			t.Fatal("accepted", value)
		}
	}
}

func TestSettingsDraftAppliesOnlyLocalEdits(t *testing.T) {
	before := Defaults()
	draft := before
	draft.Theme, draft.ShowResources, draft.HistoryRetentionSeconds = "terminal", false, 0
	other := before
	other.Language, other.RefreshSeconds, other.DefaultTab, other.ColorMode = "fr", 5, "stop", "dark"
	combined := other
	combined.Theme, combined.ShowResources, combined.HistoryRetentionSeconds = "terminal", false, 0
	for _, pair := range [][2]Settings{{draft, other}, {other, draft}} {
		got, err := pair[0].EditsFrom(before)(pair[1])
		if err != nil || got != combined {
			t.Fatalf("merged preferences: %+v, %v", got, err)
		}
	}
}

func TestThemeAndColorModeCommands(t *testing.T) {
	s := Defaults()
	var err error
	for _, cmd := range [][2]string{{"color-mode", "light"}, {"theme", "terminal"}, {"theme", "dark"}} {
		s, err = With(s, cmd[0], cmd[1])
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.Theme != "terminal" || s.ColorMode != "dark" {
		t.Fatalf("legacy command overwrote theme: %+v", s)
	}
	for _, cmd := range [][2]string{{"color-mode", "classic"}, {"theme", "invalid"}} {
		if _, err := With(s, cmd[0], cmd[1]); err == nil {
			t.Fatalf("accepted %v", cmd)
		}
	}
}
