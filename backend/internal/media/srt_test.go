package media

import (
	"strings"
	"testing"
)

func TestSRTToVTT(t *testing.T) {
	src := "\xEF\xBB\xBF1\r\n00:00:01,000 --> 00:00:02,500\r\n<i>Hello</i> {\\an8}world\r\n\r\n" +
		"2\r\n0:01:02,5 --> 0:01:04,25 X1:10 X2:20\r\nA --> B\r\n<font color=\"red\">red</font>\r\n\r\n" +
		"garbage block\r\n\r\n"
	got, err := SRTToVTT([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := "WEBVTT\n\n00:00:01.000 --> 00:00:02.500\n<i>Hello</i> world\n\n" +
		"00:01:02.500 --> 00:01:04.250\nA -> B\nred\n"
	if string(got) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSRTToVTTWindows1252(t *testing.T) {
	// "Café à l'été" in Windows-1252 (not valid UTF-8).
	src := []byte("1\n00:00:01,000 --> 00:00:02,000\nCaf\xE9 \xE0 l'\xE9t\xE9 \x93ok\x94\n")
	got, err := SRTToVTT(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Café à l'été “ok”") {
		t.Fatalf("bad decoding: %q", got)
	}
}

func TestSRTToVTTUTF16(t *testing.T) {
	text := "1\n00:00:01,000 --> 00:00:02,000\nÇa va\n"
	b := []byte{0xFF, 0xFE}
	for _, r := range text {
		b = append(b, byte(r), byte(r>>8))
	}
	got, err := SRTToVTT(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Ça va") {
		t.Fatalf("bad decoding: %q", got)
	}
}

func TestSRTToVTTEmpty(t *testing.T) {
	if _, err := SRTToVTT([]byte("not a subtitle")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestWantedLanguages(t *testing.T) {
	cases := []struct {
		user, original string
		want           string
	}{
		{"en", "en", "en"},
		{"fr", "en", "en,fr"},
		{"fr", "fr", "en"},
		{"it", "", "en,it"},
		{"de", "en", "en"},
		{"", "", "en"},
	}
	for _, c := range cases {
		if got := strings.Join(wanted(c.user, c.original), ","); got != c.want {
			t.Errorf("wanted(%q, %q) = %q, want %q", c.user, c.original, got, c.want)
		}
	}
}

func TestImdbNumber(t *testing.T) {
	for in, want := range map[string]string{"tt0032138": "32138", "0032138": "32138", "": "", "ttx": ""} {
		if got := imdbNumber(in); got != want {
			t.Errorf("imdbNumber(%q) = %q, want %q", in, got, want)
		}
	}
}
