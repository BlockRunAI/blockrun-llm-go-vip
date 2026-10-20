package vip

import (
	"strings"
	"testing"
)

func TestModelCatalogContainsLiveSnapshot(t *testing.T) {
	models := Models()
	if len(models) == 0 {
		t.Fatal("Models() returned an empty catalog")
	}

	opus, ok := FindModel("anthropic/claude-opus-5")
	if !ok {
		t.Fatal("Claude Opus 5 is missing from the catalog")
	}
	if opus.Pricing.InputPerMillion != 5 || opus.Pricing.OutputPerMillion != 25 {
		t.Fatalf("unexpected Claude Opus 5 pricing: %+v", opus.Pricing)
	}
	if opus.BillingMode != "paid" {
		t.Fatalf("unexpected Claude Opus 5 billing mode: %q", opus.BillingMode)
	}

	for _, id := range []string{
		"openai/gpt-5.6-sol", "bytedance/seedance-2.0", "elevenlabs/v3",
		"anthropic/claude-opus-5.5", "anthropic/claude-sonnet-5.5", "anthropic/claude-fable-5.1",
		"openai/gpt-6-astra", "openai/gpt-6.1-sol", "openai/gpt-5.6-sol-pro",
		"xai/grok-4.7", "zai/glm-5.3-flash", "qwen/qwen3.8-max", "google/gemini-3.8-flash",
		"xiaomi/mimo-v2.5", "nvidia/llama-3.2-11b-vision", "bytedance/seedance-2.5",
	} {
		if _, ok := FindModel(id); !ok {
			t.Errorf("%s is missing from the catalog", id)
		}
	}
}

// Ids the gateway no longer lists. The snapshot must not offer them to a model
// picker.
func TestModelCatalogOmitsRetiredIDs(t *testing.T) {
	for _, id := range []string{
		"openai/gpt-5.3",
		"nvidia/deepseek-v4-flash",
		"nvidia/qwen3-next-80b-a3b-instruct",
		"nvidia/mistral-nemotron",
		"nvidia/step-3.7-flash",
		"nvidia/seed-oss-36b",
		"nvidia/nemotron-nano-9b-v2",
		"nvidia/nemotron-nano-12b-v2-vl",
		"nvidia/nemotron-3-super-120b",
	} {
		if _, ok := FindModel(id); ok {
			t.Errorf("%s is retired but still in the catalog", id)
		}
	}
}

func TestCatalogEntriesAreWellFormed(t *testing.T) {
	seen := make(map[string]string)
	for _, model := range Models() {
		if model.Name == "" || model.Provider == "" || len(model.Categories) == 0 {
			t.Errorf("%s: incomplete entry %+v", model.ID, model)
		}
		if model.BillingMode == "" {
			t.Errorf("%s: missing billing mode", model.ID)
		}
		if !strings.Contains(model.ID, "/") {
			t.Errorf("%s: catalog ids are namespaced as provider/model", model.ID)
		}
		if prev, dup := seen[model.NativeID()]; dup {
			t.Errorf("%s and %s share the native id %q, so bare FindModel lookups are ambiguous",
				prev, model.ID, model.NativeID())
		}
		seen[model.NativeID()] = model.ID
	}
}

// The native passthrough clients take the bare provider id, so every catalog
// entry must be reachable by the same string a caller hands to Messages.New or
// Chat.Completions.New.
func TestFindModelAcceptsNativeIDs(t *testing.T) {
	opus, ok := FindModel("claude-opus-5")
	if !ok {
		t.Fatal("FindModel did not resolve the native id claude-opus-5")
	}
	if opus.ID != "anthropic/claude-opus-5" {
		t.Fatalf("resolved the wrong entry: %s", opus.ID)
	}
	if opus.NativeID() != "claude-opus-5" {
		t.Fatalf("unexpected native id: %s", opus.NativeID())
	}

	for _, model := range Models() {
		found, ok := FindModel(model.NativeID())
		if !ok {
			t.Errorf("%s: native id %q is unresolvable", model.ID, model.NativeID())
			continue
		}
		if found.ID != model.ID {
			t.Errorf("native id %q resolved to %s, want %s", model.NativeID(), found.ID, model.ID)
		}
	}

	if _, ok := FindModel("no-such-model"); ok {
		t.Error("FindModel matched an unknown id")
	}
}

func TestModelsReturnsDefensiveCopies(t *testing.T) {
	models := Models()
	models[0].Categories[0] = "mutated"
	if Models()[0].Categories[0] == "mutated" {
		t.Fatal("Models() exposed the catalog's mutable categories")
	}

	found, ok := FindModel(modelCatalog[0].ID)
	if !ok {
		t.Fatalf("%s is missing from the catalog", modelCatalog[0].ID)
	}
	found.Categories[0] = "mutated"
	if modelCatalog[0].Categories[0] == "mutated" {
		t.Fatal("FindModel exposed the catalog's mutable categories")
	}
}
