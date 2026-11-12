package textnorm

import "testing"

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"Mamba‑2":       "Mamba-2", // non-breaking hyphen, seen on real Groq
		"H‐Net – —":     "H-Net - -",
		"Mimi’s “mono”": `Mimi's "mono"`,
		"a b   c\n d":   "a b c d",
	}
	for in, want := range tests {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if Fold("Mamba‑2 Layers") != "mamba-2 layers" {
		t.Error("Fold should lowercase after normalising")
	}
}
