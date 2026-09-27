package i18n

import "testing"

func TestTDefaultsToChineseSource(t *testing.T) {
	SetLang("")
	if got := T("%d节点", 5); got != "5节点" {
		t.Fatalf("zh should be the verbatim source: %q", got)
	}
}

func TestTEnglishAndFallback(t *testing.T) {
	SetLang("en")
	defer SetLang("zh")
	if got := T("%d节点", 5); got != "5 nodes" {
		t.Fatalf("en translation: %q", got)
	}
	// A key the catalog does not have falls back to the Chinese source
	// instead of breaking the layout.
	if got := T("尚未翻译的文案"); got != "尚未翻译的文案" {
		t.Fatalf("untranslated key should fall back: %q", got)
	}
}

func TestTNoArgsSkipsSprintf(t *testing.T) {
	SetLang("en")
	defer SetLang("zh")
	// A literal % in the copy must survive: without args T never formats.
	if got := T("100% 直连"); got != "100% 直连" {
		t.Fatalf("literal %% should survive: %q", got)
	}
}
