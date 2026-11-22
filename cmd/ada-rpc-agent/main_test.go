package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestsPatchesAgentForTheWorkingDirectory(t *testing.T) {
	root := moduleRoot(".")
	if root == "" {
		t.Fatal("module root not found")
	}
	preset := filepath.Join(root, "examples", "agent", "agent.preset.json")
	workspace := filepath.Join(root, "examples", "agent", "workspace")
	ms, err := loadManifests(preset, workspace, "代码助手", "先读文件再回答", llmOverride{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var fs, prompt bool
	for _, m := range ms {
		switch m.ID {
		case "fs-local":
			fs = m.Config["workspace"] == workspace
		case "system-prompt":
			identity, _ := m.Config["identity"].(string)
			prompt = strings.Contains(identity, "代码助手") && strings.Contains(identity, "先读文件再回答")
		}
	}
	if !fs || !prompt {
		t.Errorf("fs patched=%v prompt patched=%v", fs, prompt)
	}
	if got := presetModel(ms); got != "replay" {
		t.Errorf("preset model %q, want replay", got)
	}
}

func TestProviderOverrideSwitchesPreset(t *testing.T) {
	root := moduleRoot(".")
	if root == "" {
		t.Fatal("module root not found")
	}
	preset := filepath.Join(root, "examples", "agent", "agent.preset.json")
	workspace := filepath.Join(root, "examples", "agent", "workspace")

	openaiMS, err := loadManifests(preset, workspace, "代码助手", "", llmOverride{
		Provider: "openai", Endpoint: "https://example.com/v1", Model: "mock-model",
	})
	if err != nil {
		t.Fatalf("openai override: %v", err)
	}
	if findManifest(openaiMS, "llm-openai").Disabled {
		t.Error("llm-openai should be enabled")
	}
	if !findManifest(openaiMS, "llm-replay").Disabled {
		t.Error("llm-replay should be disabled")
	}
	if got := presetModel(openaiMS); got != "mock-model" {
		t.Errorf("agent-loop model %q, want mock-model", got)
	}
	if got := findManifest(openaiMS, "llm-openai").Config["endpoint"]; got != "https://example.com/v1" {
		t.Errorf("endpoint %v", got)
	}
	if got := findManifest(openaiMS, "llm-openai").Config["apiKeyEnv"]; got != envLLMAPIKey {
		t.Errorf("apiKeyEnv %v", got)
	}

	replayMS, err := loadManifests(preset, workspace, "代码助手", "", llmOverride{Provider: "replay"})
	if err != nil {
		t.Fatalf("replay override: %v", err)
	}
	if findManifest(replayMS, "llm-replay").Disabled {
		t.Error("llm-replay should be enabled")
	}
	if !findManifest(replayMS, "llm-openai").Disabled {
		t.Error("llm-openai should be disabled")
	}
	if got := presetModel(replayMS); got != "replay" {
		t.Errorf("agent-loop model %q, want replay", got)
	}
}
