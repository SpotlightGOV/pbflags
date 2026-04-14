package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"gopkg.in/yaml.v3"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/adminclient"
	"github.com/SpotlightGOV/pbflags/internal/projectconfig"
)

func runLaunchLand(args []string) {
	fs := flag.NewFlagSet("pb launch land", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	featuresDir := fs.String("features", "", "directory of YAML config files (or .pbflags.yaml)")
	dryRun := fs.Bool("dry-run", false, "Show what would change without writing files")
	noPR := fs.Bool("no-pr", false, "Commit changes but do not create a PR")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pb launch land [--features <dir>] [--dry-run] [--no-pr] <launch-id>")
		os.Exit(1)
	}
	launchID := fs.Args()[0]

	// Resolve features directory from flag, project config, or env.
	if *featuresDir == "" {
		projCfg, projRoot, _ := projectconfig.Discover(".")
		if projCfg.FeaturesPath != "" {
			*featuresDir = projCfg.FeaturesDir(projRoot)
		}
	}
	if *featuresDir == "" {
		*featuresDir = os.Getenv("PBFLAGS_FEATURES")
	}
	if *featuresDir == "" {
		fmt.Fprintln(os.Stderr, "error: --features flag, .pbflags.yaml features_path, or PBFLAGS_FEATURES env var is required")
		os.Exit(1)
	}

	// Verify the launch is SOAKING via admin API.
	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}
	resp, err := client.GetLaunch(context.Background(), connect.NewRequest(&pbflagsv1.GetLaunchRequest{
		LaunchId: launchID,
	}))
	if err != nil {
		fatal(fmt.Errorf("get launch: %w", err))
	}
	launch := resp.Msg.GetLaunch()
	if launch == nil {
		fmt.Fprintf(os.Stderr, "error: launch %q not found\n", launchID)
		os.Exit(1)
	}
	if launch.GetStatus() != "SOAKING" {
		fmt.Fprintf(os.Stderr, "error: launch %q status is %s, must be SOAKING to land\n", launchID, launch.GetStatus())
		os.Exit(1)
	}
	if launch.GetKilledAt() != nil {
		fmt.Fprintf(os.Stderr, "error: launch %q is killed — unkill before landing\n", launchID)
		os.Exit(1)
	}

	// Find and transform all feature config files.
	entries, err := os.ReadDir(*featuresDir)
	if err != nil {
		fatal(fmt.Errorf("read features directory: %w", err))
	}

	var changedFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		path := filepath.Join(*featuresDir, entry.Name())
		changed, err := landInFeatureFile(path, launchID, *dryRun)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", path, err))
		}
		if changed {
			changedFiles = append(changedFiles, path)
		}
	}

	// Check for cross-feature launch file.
	crossFeaturePath := filepath.Join(*featuresDir, "launches", launchID+".yaml")
	if _, err := os.Stat(crossFeaturePath); err == nil {
		if *dryRun {
			fmt.Printf("would delete %s\n", crossFeaturePath)
		} else {
			if err := os.Remove(crossFeaturePath); err != nil {
				fatal(fmt.Errorf("delete cross-feature launch file: %w", err))
			}
			fmt.Printf("deleted %s\n", crossFeaturePath)
		}
		changedFiles = append(changedFiles, crossFeaturePath)
	}

	if len(changedFiles) == 0 {
		fmt.Fprintf(os.Stderr, "warning: no config files reference launch %q\n", launchID)
		os.Exit(0)
	}

	if *dryRun {
		fmt.Printf("\ndry run: %d file(s) would be modified\n", len(changedFiles))
		return
	}

	// Set status to COMPLETED.
	_, err = client.UpdateLaunchStatus(context.Background(), connect.NewRequest(&pbflagsv1.UpdateLaunchStatusRequest{
		LaunchId: launchID,
		Status:   "COMPLETED",
	}))
	if err != nil {
		fatal(fmt.Errorf("set launch status to COMPLETED: %w", err))
	}
	fmt.Printf("\n%s landed (%d file(s) modified, status set to COMPLETED)\n", launchID, len(changedFiles))

	if *noPR {
		return
	}

	// Create branch, commit, and PR.
	branchName := "launch/land-" + launchID
	if err := createLandingPR(launchID, branchName, changedFiles); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create PR: %v\n", err)
		fmt.Fprintln(os.Stderr, "Files have been modified locally. Commit and create a PR manually.")
	}
}

// landInFeatureFile transforms a single feature config file, promoting launch
// override values to base values and removing launch references. Returns true
// if the file was modified.
func landInFeatureFile(path, launchID string, dryRun bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parse YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return false, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false, nil
	}

	changed := false

	// Transform flags section.
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

	// Remove launch definition from launches section.
	if removeLaunchDefinition(root, launchID) {
		changed = true
	}

	if !changed {
		return false, nil
	}

	if dryRun {
		fmt.Printf("would modify %s\n", path)
		return true, nil
	}

	// Write back.
	out, err := yamlMarshalPreserve(&doc)
	if err != nil {
		return false, fmt.Errorf("marshal YAML: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("write file: %w", err)
	}
	fmt.Printf("modified %s\n", path)
	return true, nil
}

// landStaticFlag handles a static flag with a launch override:
//
//	value: X
//	launch:
//	  id: <launchID>
//	  value: Y
//
// Transforms to: value: Y (removes launch key).
func landStaticFlag(flagNode *yaml.Node, launchID string) bool {
	launchIdx := -1
	valueIdx := -1
	for i := 0; i < len(flagNode.Content)-1; i += 2 {
		key := flagNode.Content[i].Value
		if key == "launch" {
			launchIdx = i
		}
		if key == "value" {
			valueIdx = i
		}
	}
	if launchIdx < 0 || valueIdx < 0 {
		return false
	}

	launchNode := flagNode.Content[launchIdx+1]
	if launchNode.Kind != yaml.MappingNode {
		return false
	}

	// Check if this launch override matches our launch ID.
	idNode := yamlMapLookup(launchNode, "id")
	if idNode == nil || idNode.Value != launchID {
		return false
	}

	// Get the launch override value.
	overrideValueNode := yamlMapLookup(launchNode, "value")
	if overrideValueNode == nil {
		return false
	}

	// Replace the base value with the override value.
	*flagNode.Content[valueIdx+1] = *overrideValueNode

	// Remove the launch key-value pair.
	flagNode.Content = append(flagNode.Content[:launchIdx], flagNode.Content[launchIdx+2:]...)
	return true
}

// landConditions handles condition chains with launch overrides.
func landConditions(flagNode *yaml.Node, launchID string) bool {
	conditionsNode := yamlMapLookup(flagNode, "conditions")
	if conditionsNode == nil || conditionsNode.Kind != yaml.SequenceNode {
		return false
	}

	changed := false
	for _, condNode := range conditionsNode.Content {
		if condNode.Kind != yaml.MappingNode {
			continue
		}
		if landCondition(condNode, launchID) {
			changed = true
		}
	}
	return changed
}

// landCondition transforms a single condition entry:
//
//   - when: "expr"
//     value: X
//     launch:
//     id: <launchID>
//     value: Y
//
// Transforms to: - when: "expr"  value: Y
func landCondition(condNode *yaml.Node, launchID string) bool {
	launchIdx := -1
	valueIdx := -1
	otherwiseIdx := -1
	for i := 0; i < len(condNode.Content)-1; i += 2 {
		key := condNode.Content[i].Value
		switch key {
		case "launch":
			launchIdx = i
		case "value":
			valueIdx = i
		case "otherwise":
			otherwiseIdx = i
		}
	}
	if launchIdx < 0 {
		return false
	}

	launchNode := condNode.Content[launchIdx+1]
	if launchNode.Kind != yaml.MappingNode {
		return false
	}

	idNode := yamlMapLookup(launchNode, "id")
	if idNode == nil || idNode.Value != launchID {
		return false
	}

	overrideValueNode := yamlMapLookup(launchNode, "value")
	if overrideValueNode == nil {
		return false
	}

	// Replace value (or otherwise) with the override value.
	targetIdx := valueIdx
	if targetIdx < 0 {
		targetIdx = otherwiseIdx
	}
	if targetIdx < 0 {
		return false
	}

	*condNode.Content[targetIdx+1] = *overrideValueNode

	// Remove the launch key-value pair.
	condNode.Content = append(condNode.Content[:launchIdx], condNode.Content[launchIdx+2:]...)
	return true
}

// removeLaunchDefinition removes a launch entry from the launches: section.
// If the launches section becomes empty, it is removed entirely.
func removeLaunchDefinition(root *yaml.Node, launchID string) bool {
	launchesIdx := -1
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == "launches" {
			launchesIdx = i
			break
		}
	}
	if launchesIdx < 0 {
		return false
	}

	launchesNode := root.Content[launchesIdx+1]
	if launchesNode.Kind != yaml.MappingNode {
		return false
	}

	// Find and remove the launch entry.
	for i := 0; i < len(launchesNode.Content)-1; i += 2 {
		if launchesNode.Content[i].Value == launchID {
			launchesNode.Content = append(launchesNode.Content[:i], launchesNode.Content[i+2:]...)

			// If launches section is now empty, remove it from root.
			if len(launchesNode.Content) == 0 {
				root.Content = append(root.Content[:launchesIdx], root.Content[launchesIdx+2:]...)
			}
			return true
		}
	}
	return false
}

// yamlMapLookup finds a value node by key in a mapping node.
func yamlMapLookup(mapping *yaml.Node, key string) *yaml.Node {
	if mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// yamlMarshalPreserve marshals a yaml.Node document, preserving structure.
func yamlMarshalPreserve(doc *yaml.Node) ([]byte, error) {
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// createLandingPR creates a git branch, commits changes, and opens a PR.
func createLandingPR(launchID, branchName string, changedFiles []string) error {
	// Create and switch to branch.
	if err := runGit("checkout", "-b", branchName); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}

	// Stage changed files (skip deleted files — they need git rm).
	for _, f := range changedFiles {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			if err := runGit("rm", f); err != nil {
				return fmt.Errorf("git rm %s: %w", f, err)
			}
		} else {
			if err := runGit("add", f); err != nil {
				return fmt.Errorf("git add %s: %w", f, err)
			}
		}
	}

	// Commit.
	msg := fmt.Sprintf("Launch land: %s\n\nPromote launch override values to defaults and remove launch definition.", launchID)
	if err := runGit("commit", "-m", msg); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Push.
	if err := runGit("push", "-u", "origin", branchName); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	// Create PR via gh.
	title := fmt.Sprintf("Launch land: %s", launchID)
	body := fmt.Sprintf("## Summary\n\n- Promote launch `%s` override values to defaults\n- Remove launch definition from config\n- Launch status set to COMPLETED\n\n## Changed files\n\n", launchID)
	for _, f := range changedFiles {
		body += fmt.Sprintf("- `%s`\n", f)
	}
	if err := runCmd("gh", "pr", "create", "--title", title, "--body", body); err != nil {
		return fmt.Errorf("create PR: %w", err)
	}

	return nil
}

// runGit runs a git command, returning an error if it fails.
func runGit(args ...string) error {
	return runCmd("git", args...)
}

// runCmd runs an external command, printing its output.
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
