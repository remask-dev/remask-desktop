package stream

import "testing"

func TestRestorerHandlesTokenAcrossDeltas(t *testing.T) {
	restorer := NewRestorer(func(token string) (string, bool) {
		if token == "<PHONE_NUMBER:A7F2>" {
			return "13800138000", true
		}
		return "", false
	})
	if got := restorer.Feed("choice:0", "请联系 <PHONE_"); got != "请联系 " {
		t.Fatalf("unexpected first delta: %q", got)
	}
	if got := restorer.Feed("choice:0", "NUMBER:A7F2>，谢谢"); got != "13800138000，谢谢" {
		t.Fatalf("unexpected second delta: %q", got)
	}
}

func TestRestorerSeparatesChannels(t *testing.T) {
	restorer := NewRestorer(func(string) (string, bool) { return "", false })
	if got := restorer.Feed("a", "<PHONE_"); got != "" {
		t.Fatalf("unexpected output: %q", got)
	}
	if got := restorer.Feed("b", "NUMBER:A7F2>"); got != "NUMBER:A7F2>" {
		t.Fatalf("channels were mixed: %q", got)
	}
	if got := restorer.Flush("a"); got != "<PHONE_" {
		t.Fatalf("unexpected flush: %q", got)
	}
}
