package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLandStaticFlag(t *testing.T) {
	input := `feature: notifications
flags:
  score_threshold:
    value: 0.75
    launch:
      id: scoring_experiment
      value: 0.5
`
	want := `feature: notifications
flags:
  score_threshold:
    value: 0.5
`
	testLandTransform(t, input, "scoring_experiment", want)
}

func TestLandConditionOverride(t *testing.T) {
	input := `feature: notifications
flags:
  email_enabled:
    conditions:
      - when: "ctx.plan == PlanLevel.PRO"
        value: false
        launch:
          id: email_rollout
          value: true
      - otherwise: false
`
	want := `feature: notifications
flags:
  email_enabled:
    conditions:
      - when: "ctx.plan == PlanLevel.PRO"
        value: true
      - otherwise: false
`
	testLandTransform(t, input, "email_rollout", want)
}

func TestLandRemovesLaunchDefinition(t *testing.T) {
	input := `feature: notifications
launches:
  my_launch:
    dimension: user_id
    ramp_percentage: 25
    description: "Roll out feature"
flags:
  feature_enabled:
    value: false
    launch:
      id: my_launch
      value: true
`
	want := `feature: notifications
flags:
  feature_enabled:
    value: true
`
	testLandTransform(t, input, "my_launch", want)
}

func TestLandRemovesOnlyTargetLaunch(t *testing.T) {
	input := `feature: notifications
launches:
  my_launch:
    dimension: user_id
    ramp_percentage: 25
  other_launch:
    dimension: session_id
    ramp_percentage: 50
flags:
  feature_enabled:
    value: false
    launch:
      id: my_launch
      value: true
`
	want := `feature: notifications
launches:
  other_launch:
    dimension: session_id
    ramp_percentage: 50
flags:
  feature_enabled:
    value: true
`
	testLandTransform(t, input, "my_launch", want)
}

func TestLandNoMatchingLaunch(t *testing.T) {
	input := `feature: notifications
flags:
  score_threshold:
    value: 0.75
    launch:
      id: other_launch
      value: 0.5
`
	testLandTransformUnchanged(t, input, "nonexistent_launch")
}

func TestLandMultipleConditions(t *testing.T) {
	input := `feature: notifications
flags:
  digest_frequency:
    conditions:
      - when: "ctx.plan == PlanLevel.ENTERPRISE"
        value: "hourly"
      - when: "ctx.plan == PlanLevel.PRO"
        value: "daily"
        launch:
          id: hourly_rollout
          value: "hourly"
      - otherwise: "weekly"
`
	want := `feature: notifications
flags:
  digest_frequency:
    conditions:
      - when: "ctx.plan == PlanLevel.ENTERPRISE"
        value: "hourly"
      - when: "ctx.plan == PlanLevel.PRO"
        value: "hourly"
      - otherwise: "weekly"
`
	testLandTransform(t, input, "hourly_rollout", want)
}

func TestLandMultipleFlagsAndLaunches(t *testing.T) {
	input := `feature: notifications
launches:
  rollout_a:
    dimension: user_id
    ramp_percentage: 100
flags:
  flag_one:
    value: false
    launch:
      id: rollout_a
      value: true
  flag_two:
    conditions:
      - when: "ctx.is_internal"
        value: 10
        launch:
          id: rollout_a
          value: 20
      - otherwise: 5
`
	want := `feature: notifications
flags:
  flag_one:
    value: true
  flag_two:
    conditions:
      - when: "ctx.is_internal"
        value: 20
      - otherwise: 5
`
	testLandTransform(t, input, "rollout_a", want)
}

func TestLandInFeatureFile(t *testing.T) {
	dir := t.TempDir()
	input := `feature: notifications
launches:
  my_launch:
    dimension: user_id
    ramp_percentage: 100
flags:
  feature_on:
    value: false
    launch:
      id: my_launch
      value: true
`
	path := filepath.Join(dir, "notifications.yaml")
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := landInFeatureFile(path, "my_launch", false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected file to be changed")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	want := `feature: notifications
flags:
  feature_on:
    value: true
`
	assertYAMLEqual(t, want, string(got))
}

func TestLandInFeatureFileDryRun(t *testing.T) {
	dir := t.TempDir()
	input := `feature: notifications
flags:
  feature_on:
    value: false
    launch:
      id: my_launch
      value: true
`
	path := filepath.Join(dir, "notifications.yaml")
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := landInFeatureFile(path, "my_launch", true)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true in dry run")
	}

	// File should be unchanged in dry run.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != input {
		t.Fatalf("file should not be modified in dry run\ngot:\n%s", got)
	}
}

func TestLandCrossFeatureFile(t *testing.T) {
	dir := t.TempDir()

	// Create feature config referencing the cross-feature launch.
	featureInput := `feature: notifications
flags:
  score_threshold:
    value: 0.75
    launch:
      id: scoring_experiment
      value: 0.5
`
	featurePath := filepath.Join(dir, "notifications.yaml")
	if err := os.WriteFile(featurePath, []byte(featureInput), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create cross-feature launch file.
	launchDir := filepath.Join(dir, "launches")
	if err := os.MkdirAll(launchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	launchInput := `dimension: session_id
ramp_percentage: 50
description: "Test lower score threshold"
`
	launchPath := filepath.Join(launchDir, "scoring_experiment.yaml")
	if err := os.WriteFile(launchPath, []byte(launchInput), 0o644); err != nil {
		t.Fatal(err)
	}

	// Transform the feature file.
	changed, err := landInFeatureFile(featurePath, "scoring_experiment", false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected feature file to be changed")
	}

	// Verify the cross-feature launch file still exists (land command handles deletion separately).
	if _, err := os.Stat(launchPath); err != nil {
		t.Fatal("cross-feature launch file should still exist after landInFeatureFile")
	}

	// Verify feature file was transformed.
	got, err := os.ReadFile(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	want := `feature: notifications
flags:
  score_threshold:
    value: 0.5
`
	assertYAMLEqual(t, want, string(got))
}

// testLandTransform applies the land transformation and asserts the result.
func testLandTransform(t *testing.T, input, launchID, want string) {
	t.Helper()

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(input), &doc); err != nil {
		t.Fatalf("parse input YAML: %v", err)
	}
	root := doc.Content[0]

	flagsNode := yamlMapLookup(root, "flags")
	if flagsNode != nil && flagsNode.Kind == yaml.MappingNode {
		for i := 0; i < len(flagsNode.Content)-1; i += 2 {
			flagValueNode := flagsNode.Content[i+1]
			if flagValueNode.Kind != yaml.MappingNode {
				continue
			}
			landStaticFlag(flagValueNode, launchID)
			landConditions(flagValueNode, launchID)
		}
	}
	removeLaunchDefinition(root, launchID)

	got, err := yamlMarshalPreserve(&doc)
	if err != nil {
		t.Fatalf("marshal YAML: %v", err)
	}

	assertYAMLEqual(t, want, string(got))
}

// testLandTransformUnchanged verifies no changes are made.
func testLandTransformUnchanged(t *testing.T, input, launchID string) {
	t.Helper()

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(input), &doc); err != nil {
		t.Fatalf("parse input YAML: %v", err)
	}
	root := doc.Content[0]

	changed := false
	flagsNode := yamlMapLookup(root, "flags")
	if flagsNode != nil && flagsNode.Kind == yaml.MappingNode {
		for i := 0; i < len(flagsNode.Content)-1; i += 2 {
			flagValueNode := flagsNode.Content[i+1]
			if flagValueNode.Kind != yaml.MappingNode {
				continue
			}
			if landStaticFlag(flagValueNode, launchID) {
				changed = true
			}
			if landConditions(flagValueNode, launchID) {
				changed = true
			}
		}
	}
	if removeLaunchDefinition(root, launchID) {
		changed = true
	}

	if changed {
		t.Fatal("expected no changes for non-matching launch ID")
	}
}

// assertYAMLEqual compares two YAML strings after normalizing whitespace.
func assertYAMLEqual(t *testing.T, want, got string) {
	t.Helper()
	wantNorm := strings.TrimSpace(want)
	gotNorm := strings.TrimSpace(got)
	if wantNorm != gotNorm {
		t.Errorf("YAML mismatch:\nwant:\n%s\n\ngot:\n%s", wantNorm, gotNorm)
	}
}
