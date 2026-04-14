package projectconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscover_Found(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, FileName), []byte("features_path: features\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, root, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if root != dir {
		t.Errorf("root = %q, want %q", root, dir)
	}
	if cfg.FeaturesPath != "features" {
		t.Errorf("features_path = %q, want %q", cfg.FeaturesPath, "features")
	}
}

func TestDiscover_WalksUp(t *testing.T) {
	root := t.TempDir()
	err := os.WriteFile(filepath.Join(root, FileName), []byte("features_path: feat\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b", "c")
	os.MkdirAll(sub, 0755)

	cfg, found, err := Discover(sub)
	if err != nil {
		t.Fatal(err)
	}
	if found != root {
		t.Errorf("root = %q, want %q", found, root)
	}
	if cfg.FeaturesPath != "feat" {
		t.Errorf("features_path = %q, want %q", cfg.FeaturesPath, "feat")
	}
}

func TestDiscover_NotFound(t *testing.T) {
	dir := t.TempDir()
	cfg, root, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if root != "" {
		t.Errorf("expected empty root, got %q", root)
	}
	if cfg.FeaturesPath != "" {
		t.Errorf("expected empty features_path, got %q", cfg.FeaturesPath)
	}
}

func TestFeaturesDir(t *testing.T) {
	cfg := Config{FeaturesPath: "features"}
	got := cfg.FeaturesDir("/home/user/repo")
	want := "/home/user/repo/features"
	if got != want {
		t.Errorf("FeaturesDir = %q, want %q", got, want)
	}
}

func TestFeaturesDir_Absolute(t *testing.T) {
	cfg := Config{FeaturesPath: "/absolute/path"}
	got := cfg.FeaturesDir("/home/user/repo")
	if got != "/absolute/path" {
		t.Errorf("FeaturesDir = %q, want /absolute/path", got)
	}
}

func TestFeaturesDir_Empty(t *testing.T) {
	cfg := Config{}
	got := cfg.FeaturesDir("/home/user/repo")
	if got != "" {
		t.Errorf("FeaturesDir = %q, want empty", got)
	}
}

func TestDescriptorsFile(t *testing.T) {
	cfg := Config{DescriptorsPath: "descriptors.pb"}
	got := cfg.DescriptorsFile("/home/user/repo")
	want := "/home/user/repo/descriptors.pb"
	if got != want {
		t.Errorf("DescriptorsFile = %q, want %q", got, want)
	}
}

func TestDescriptorsFile_Absolute(t *testing.T) {
	cfg := Config{DescriptorsPath: "/absolute/descriptors.pb"}
	got := cfg.DescriptorsFile("/home/user/repo")
	if got != "/absolute/descriptors.pb" {
		t.Errorf("DescriptorsFile = %q, want /absolute/descriptors.pb", got)
	}
}

func TestDescriptorsFile_Empty(t *testing.T) {
	cfg := Config{}
	got := cfg.DescriptorsFile("/home/user/repo")
	if got != "" {
		t.Errorf("DescriptorsFile = %q, want empty", got)
	}
}

func TestProtoDir(t *testing.T) {
	cfg := Config{ProtoPath: "proto"}
	got := cfg.ProtoDir("/home/user/repo")
	want := "/home/user/repo/proto"
	if got != want {
		t.Errorf("ProtoDir = %q, want %q", got, want)
	}
}

func TestProtoDir_Empty(t *testing.T) {
	cfg := Config{}
	got := cfg.ProtoDir("/home/user/repo")
	if got != "" {
		t.Errorf("ProtoDir = %q, want empty", got)
	}
}

func TestDiscover_AllFields(t *testing.T) {
	dir := t.TempDir()
	content := "features_path: features\ndescriptors_path: desc.pb\nproto_path: proto\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FeaturesPath != "features" {
		t.Errorf("FeaturesPath = %q", cfg.FeaturesPath)
	}
	if cfg.DescriptorsPath != "desc.pb" {
		t.Errorf("DescriptorsPath = %q", cfg.DescriptorsPath)
	}
	if cfg.ProtoPath != "proto" {
		t.Errorf("ProtoPath = %q", cfg.ProtoPath)
	}
}
