package pdf

import "testing"

func TestNormalizeCollapsesDotLeaders(t *testing.T) {
	in := "5.2.5 Assigned Rules ................................ 32\n" +
		"Message Header . . . . . . . . . . . . . . 16"
	got := normalize(in)
	want := "5.2.5 Assigned Rules 32\nMessage Header 16"
	if got != want {
		t.Fatalf("normalize:\n got %q\nwant %q", got, want)
	}
}

func TestNormalizeKeepsVersionsAndEllipsis(t *testing.T) {
	in := "La versión 2.2.0.5 sigue... intacta."
	if got := normalize(in); got != in {
		t.Fatalf("normalize alteró texto legítimo: %q", got)
	}
}
