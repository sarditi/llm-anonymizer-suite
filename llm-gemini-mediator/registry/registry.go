package registry

import (
	"llm-gemini-mediator/registry/providers"
)

// Reflection-like initiation for data source, dispatchers and an handler based on configuration values
type Creator func() interface{}

var registry = make(map[string]Creator)

// Register your types
func init() {
    registry["MOCK"] = func() interface{} { return &providers.MockProvider{} }
    registry["SIMPLE_GEMINI"] = func() interface{} { return &providers.GeminiProvider{} }
}

func CreateInstance(name string) interface{} {
    if fn, ok := registry[name]; ok {
        return fn()
    }
    return nil
}