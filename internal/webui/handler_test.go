package webui

import (
	"strings"
	"testing"
)

func TestWelcomeHTMLReferencesCurrentRepository(t *testing.T) {
	if !strings.Contains(welcomeHTML, "https://github.com/chm413/DeepSeek_Web_To_API") {
		t.Fatal("welcome page must link to the current repository")
	}
	if strings.Contains(strings.ToLower(welcomeHTML), "meow-calculations") {
		t.Fatal("welcome page must not link to the archived upstream repository")
	}
}
