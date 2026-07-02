package ci_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseWorkflowCreatesGeneratedNotesFromVersionTag(t *testing.T) {
	workflow := readYAMLFile(t, ".github/workflows/release.yml")

	requireScalar(t, workflow, "name", "Release")
	requireScalar(t, mappingValue(t, workflow, "permissions"), "contents", "write")

	on := mappingValue(t, workflow, "on")
	push := mappingValue(t, on, "push")
	tags := sequenceScalars(t, mappingValue(t, push, "tags"))
	if !contains(tags, "v*") {
		t.Fatalf("release workflow push tags = %v, want v*", tags)
	}

	dispatch := mappingValue(t, on, "workflow_dispatch")
	inputs := mappingValue(t, dispatch, "inputs")
	tagInput := mappingValue(t, inputs, "tag")
	requireScalar(t, tagInput, "required", "true")
	requireScalar(t, tagInput, "type", "string")

	jobs := mappingValue(t, workflow, "jobs")
	release := mappingValue(t, jobs, "release")
	requireScalar(t, release, "runs-on", "ubuntu-latest")

	steps := mappingValue(t, release, "steps")
	if !stepUses(steps, "actions/checkout@") {
		t.Fatal("release workflow must check out source with actions/checkout")
	}
	if !stepUses(steps, "actions/setup-go@") {
		t.Fatal("release workflow must set up Go with actions/setup-go")
	}
	if !stepWithUsesHasScalar(steps, "actions/setup-go@", "with", "go-version-file", "go.mod") {
		t.Fatal("actions/setup-go step must use go.mod as the Go version source")
	}
	if !stepRuns(steps, "go test ./...") {
		t.Fatal("release workflow must run the full Go test suite before publishing")
	}
	if !stepRuns(steps, "gh release create") {
		t.Fatal("release workflow must create the GitHub release with gh")
	}
	if !stepRuns(steps, "--generate-notes") {
		t.Fatal("release workflow must ask GitHub to generate release notes")
	}
	if !stepRuns(steps, "--verify-tag") {
		t.Fatal("release workflow must verify the release tag already exists")
	}
}

func TestReleaseNotesConfigurationGroupsChangelogEntries(t *testing.T) {
	config := readYAMLFile(t, ".github/release.yml")
	changelog := mappingValue(t, config, "changelog")
	exclude := mappingValue(t, changelog, "exclude")
	excludedLabels := sequenceScalars(t, mappingValue(t, exclude, "labels"))
	if !contains(excludedLabels, "ignore-for-release") {
		t.Fatalf("excluded changelog labels = %v, want ignore-for-release", excludedLabels)
	}

	categories := mappingValue(t, changelog, "categories")
	requiredTitles := []string{
		"Breaking Changes",
		"Features",
		"Bug Fixes",
		"CI",
		"Maintenance",
		"Other Changes",
	}
	for _, title := range requiredTitles {
		if !categoryExists(categories, title) {
			t.Fatalf("release notes categories must include %q", title)
		}
	}
	if !categoryHasLabel(categories, "Other Changes", "*") {
		t.Fatal("Other Changes release notes category must catch uncategorized pull requests")
	}
}

func readYAMLFile(t *testing.T, path string) *yaml.Node {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s as YAML: %v", path, err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("%s must contain a YAML mapping document", path)
	}
	return doc.Content[0]
}

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repository root containing go.mod")
		}
		dir = parent
	}
}

func mappingValue(t *testing.T, mapping *yaml.Node, key string) *yaml.Node {
	t.Helper()

	if mapping == nil || mapping.Kind != yaml.MappingNode {
		t.Fatalf("expected YAML mapping while looking for key %q", key)
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	t.Fatalf("missing YAML key %q", key)
	return nil
}

func requireScalar(t *testing.T, mapping *yaml.Node, key, want string) {
	t.Helper()

	node := mappingValue(t, mapping, key)
	if node.Kind != yaml.ScalarNode {
		t.Fatalf("%q must be a scalar, got kind %d", key, node.Kind)
	}
	if node.Value != want {
		t.Fatalf("%q = %q, want %q", key, node.Value, want)
	}
}

func sequenceScalars(t *testing.T, sequence *yaml.Node) []string {
	t.Helper()

	if sequence.Kind != yaml.SequenceNode {
		t.Fatalf("expected YAML sequence, got kind %d", sequence.Kind)
	}
	values := make([]string, 0, len(sequence.Content))
	for _, node := range sequence.Content {
		if node.Kind != yaml.ScalarNode {
			t.Fatalf("sequence item must be a scalar, got kind %d", node.Kind)
		}
		values = append(values, node.Value)
	}
	return values
}

func stepUses(steps *yaml.Node, prefix string) bool {
	return anyStep(steps, func(step *yaml.Node) bool {
		uses := optionalMappingValue(step, "uses")
		return uses != nil && strings.HasPrefix(uses.Value, prefix)
	})
}

func stepRuns(steps *yaml.Node, text string) bool {
	return anyStep(steps, func(step *yaml.Node) bool {
		run := optionalMappingValue(step, "run")
		return run != nil && strings.Contains(run.Value, text)
	})
}

func stepWithUsesHasScalar(steps *yaml.Node, usesPrefix, mappingKey, scalarKey, want string) bool {
	return anyStep(steps, func(step *yaml.Node) bool {
		uses := optionalMappingValue(step, "uses")
		if uses == nil || !strings.HasPrefix(uses.Value, usesPrefix) {
			return false
		}
		nested := optionalMappingValue(step, mappingKey)
		if nested == nil {
			return false
		}
		scalar := optionalMappingValue(nested, scalarKey)
		return scalar != nil && scalar.Value == want
	})
}

func anyStep(steps *yaml.Node, match func(*yaml.Node) bool) bool {
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return false
	}
	for _, step := range steps.Content {
		if step.Kind == yaml.MappingNode && match(step) {
			return true
		}
	}
	return false
}

func optionalMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func categoryExists(categories *yaml.Node, title string) bool {
	return anyCategory(categories, title, func(*yaml.Node) bool { return true })
}

func categoryHasLabel(categories *yaml.Node, title, label string) bool {
	return anyCategory(categories, title, func(category *yaml.Node) bool {
		labels := optionalMappingValue(category, "labels")
		if labels == nil {
			return false
		}
		for _, got := range labels.Content {
			if got.Value == label {
				return true
			}
		}
		return false
	})
}

func anyCategory(categories *yaml.Node, title string, match func(*yaml.Node) bool) bool {
	if categories == nil || categories.Kind != yaml.SequenceNode {
		return false
	}
	for _, category := range categories.Content {
		if category.Kind != yaml.MappingNode {
			continue
		}
		titleNode := optionalMappingValue(category, "title")
		if titleNode != nil && titleNode.Value == title && match(category) {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
