package meta

import "strings"

// DefaultTestModel is the lightweight Muse model used for connection tests.
const DefaultTestModel = "muse-ember-1.0"

// ModelDescriptor describes a Meta Muse model.
type ModelDescriptor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// DefaultModels returns the curated Meta Muse model catalog aligned with CLIProxyAPI.
func DefaultModels() []ModelDescriptor {
	return []ModelDescriptor{
		{ID: "muse-ember-1.0", DisplayName: "Muse Ember 1.0"},
		{ID: "muse-wonder-1.0", DisplayName: "Muse Wonder 1.0"},
		{ID: "muse-wonder-1.0-contributor", DisplayName: "Muse Wonder 1.0 Contributor"},
		{ID: "muse-wonder-full-1.0", DisplayName: "Muse Wonder Full 1.0"},
		{ID: "muse-wonder-full-1.0-contributor", DisplayName: "Muse Wonder Full 1.0 Contributor"},
		{ID: "muse-spark-1.3", DisplayName: "Muse Spark 1.3"},
		{ID: "muse-spark-1.3-contributor", DisplayName: "Muse Spark 1.3 Contributor"},
		{ID: "muse-spark-1.2", DisplayName: "Muse Spark 1.2"},
		{ID: "muse-spark-1.2-contributor", DisplayName: "Muse Spark 1.2 Contributor"},
		{ID: "muse-spark-1.1", DisplayName: "Muse Spark 1.1"},
	}
}

// DefaultModelIDs returns the model IDs of DefaultModels.
func DefaultModelIDs() []string {
	models := DefaultModels()
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.ID
	}
	return out
}

// IsContributorModel reports whether model requests Muse contributor mode.
func IsContributorModel(model string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), "-contributor")
}
