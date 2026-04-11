package flagfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandArgs_noAtFiles(t *testing.T) {
	args := []string{"--database=postgres://localhost", "--listen=:9200"}
	got, err := ExpandArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != args[0] || got[1] != args[1] {
		t.Fatalf("expected passthrough, got %v", got)
	}
}

func TestExpandArgs_expandsAtFile(t *testing.T) {
	dir := t.TempDir()

	// Disable home overrides for this test.
	t.Setenv("PBFLAGS_OVERRIDES_DIR", dir)

	flagFile := filepath.Join(dir, "config.flags")
	content := `# database config
"--database=postgres://user:pass@db:5432/flags"
--listen=:9200

# cache tuning
--cache-flag-ttl=5m
`
	if err := os.WriteFile(flagFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ExpandArgs([]string{"@" + flagFile, "--standalone"})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"--database=postgres://user:pass@db:5432/flags",
		"--listen=:9200",
		"--cache-flag-ttl=5m",
		"--standalone",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d args, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExpandArgs_missingFile(t *testing.T) {
	t.Setenv("PBFLAGS_OVERRIDES_DIR", t.TempDir())
	_, err := ExpandArgs([]string{"@/nonexistent/file.flags"})
	if err == nil {
		t.Fatal("expected error for missing @file")
	}
}

func TestMergeFlags(t *testing.T) {
	base := []string{"--database=old", "--listen=:9200"}
	overrides := []string{"--database=new", "--env-name=prod"}

	got := mergeFlags(base, overrides)

	want := []string{"--database=new", "--listen=:9200", "--env-name=prod"}
	if len(got) != len(want) {
		t.Fatalf("got %d args, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFlagName(t *testing.T) {
	tests := []struct {
		arg  string
		want string
	}{
		{"--database=postgres://...", "database"},
		{"--listen", "listen"},
		{"--cache-flag-ttl=5m", "cache-flag-ttl"},
		{"positional", ""},
		{`"--quoted=val"`, "quoted"},
	}
	for _, tt := range tests {
		if got := flagName(tt.arg); got != tt.want {
			t.Errorf("flagName(%q) = %q, want %q", tt.arg, got, tt.want)
		}
	}
}

func TestApplyHomeOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PBFLAGS_OVERRIDES_DIR", dir)

	bin := filepath.Base(os.Args[0])
	overrideFile := filepath.Join(dir, bin+".flags")
	if err := os.WriteFile(overrideFile, []byte("--database=override\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := applyHomeOverrides([]string{"--database=original", "--listen=:9200"})
	if err != nil {
		t.Fatal(err)
	}

	// database should be replaced, listen should remain.
	if len(got) != 2 {
		t.Fatalf("expected 2 args, got %v", got)
	}
	if got[0] != "--database=override" {
		t.Errorf("expected override, got %q", got[0])
	}
	if got[1] != "--listen=:9200" {
		t.Errorf("expected listen unchanged, got %q", got[1])
	}
}
