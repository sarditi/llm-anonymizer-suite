package registry

import (
	"ext-llm-gateway/registry/datasource"
	"ext-llm-gateway/registry/dispatchers"
	"ext-llm-gateway/registry/handlers"

)
// Reflectio-lioke initiation for data source, dispatchers and an handler based on configuration values
type Creator func() interface{}

var registry = make(map[string]Creator)

// Register your types
func init() {
    registry["Redis"] = func() interface{} { return &ds.RedisDatasource{} }
    registry["REDISACL_RESTAPI"] = func() interface{} { return &dispatchers.RestAPIDispatcher{} }
    registry["REDISACL_FINALIZER"] = func() interface{} { return &dispatchers.RedisAclFinalizer{} }
	registry["GINRESTAPI"] = func() interface{} { return &handlers.RestAPIHandler{} }
}

func CreateInstance(name string) interface{} {
    if fn, ok := registry[name]; ok {
        return fn()
    }
    return nil
}