package telegram

import "testing"

func TestClassifyDocumentKeepsTextOutOfPhoto(t *testing.T) {
	cases := []struct {
		name, mime string
		want       DocumentKind
	}{
		{"profile.md", "image/jpeg", DocumentText},
		{"profile.json", "application/json", DocumentJSON},
		{"meal.jpg", "image/jpeg", DocumentPhoto},
		{"notes.md", "text/markdown", DocumentText},
		{"archive.zip", "application/zip", DocumentOther},
	}
	for _, c := range cases {
		if got := ClassifyDocument(c.name, c.mime); got != c.want {
			t.Fatalf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
