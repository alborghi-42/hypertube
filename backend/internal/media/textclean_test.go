package media

import "testing"

func TestPlainText(t *testing.T) {
	in := `<p style="margin:0px;"><span>Documental sobre la ciutat</span></p><p><span><br /></span></p><p><b><i>Documentary &amp; &quot;La Patum&quot;</i></b></p>`
	want := "Documental sobre la ciutat\n\nDocumentary & \"La Patum\""
	if got := PlainText(in); got != want {
		t.Fatalf("PlainText = %q, want %q", got, want)
	}
	if got := PlainText("  plain text  "); got != "plain text" {
		t.Fatalf("PlainText(plain) = %q", got)
	}
}

func TestParseArchiveRuntime(t *testing.T) {
	for in, want := range map[string]float64{"12'": 720, "85 min": 5100, "1:23:45": 5025, "12:03": 723, "1h 25m": 5100, "": 0, "n/a": 0} {
		if got := parseArchiveRuntime(in); got != want {
			t.Errorf("parseArchiveRuntime(%q) = %v, want %v", in, got, want)
		}
	}
	files := []archiveMetadataFile{{Name: "a.mp4", Length: "713.62"}, {Name: "b.ogv", Length: "850.13"}, {Name: "c.jpg", Length: "9999"}}
	if got := archiveDuration("", files); got != 850.13 {
		t.Errorf("archiveDuration = %v, want 850.13", got)
	}
}
