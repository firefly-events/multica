package hive

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed catalog/skills-catalog.json
var skillCatalogJSON []byte

// CatalogSkill is one entry in the versioned Hive skill catalog.
type CatalogSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	WhenToUse   string `json:"when_to_use"`
}

// SkillCatalog is the top-level structure of the embedded catalog.
type SkillCatalog struct {
	Version string         `json:"version"`
	Skills  []CatalogSkill `json:"skills"`
}

// loadCatalog parses the embedded skill catalog JSON.
// Panics on parse failure — the JSON is compiled into the binary so a
// failure means a broken build artifact.
func loadCatalog() SkillCatalog {
	var c SkillCatalog
	if err := json.Unmarshal(skillCatalogJSON, &c); err != nil {
		panic(fmt.Sprintf("hive: parse embedded skill catalog: %v", err))
	}
	return c
}
