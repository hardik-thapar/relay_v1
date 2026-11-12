package standin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOverridesOnlyGivenFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "halved.json")
	if err := os.WriteFile(path, []byte(`{"RPM": 15, "TPM": 4000}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if cfg.RPM != 15 || cfg.TPM != 4000 {
		t.Errorf("overrides not applied: RPM=%d TPM=%d", cfg.RPM, cfg.TPM)
	}
	if cfg.RPD != d.RPD || len(cfg.FirstTokenMS) != len(d.FirstTokenMS) || cfg.Source != d.Source {
		t.Error("fields not in the file must keep their calibrated defaults")
	}
}

func TestLoadRejectsBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(path, []byte(`{"RPM": `), 0o644)
	if _, err := Load(path); err == nil {
		t.Error("want error for truncated JSON")
	}
}
