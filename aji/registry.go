package aji

import (
	"fmt"
	"reflect"
	"sync"
)

var (
	// registry maps function name → registeredFn.
	registry sync.Map
	// reverseMap maps function pointer (uintptr) → function name.
	// Used by Map() to find the registered name from a function value.
	reverseMap sync.Map
)

// registeredFn holds the reflected type information and function value for
// a registered burst function.
type registeredFn struct {
	inputType  reflect.Type
	outputType reflect.Type
	fn         any // func(T)(U, error) stored as interface{}
}

// Register registers a function for use as a burst worker.
// Must be called before Map() or Setup(), typically from init() or main().
//
// fn must be of type func(T) (U, error) where T and U are JSON-serializable types.
// The name must be unique within the program.
func Register[T, U any](name string, fn func(T) (U, error)) {
	entry := registeredFn{
		inputType:  reflect.TypeOf((*T)(nil)).Elem(),
		outputType: reflect.TypeOf((*U)(nil)).Elem(),
		fn:         fn,
	}
	registry.Store(name, entry)
	reverseMap.Store(reflect.ValueOf(fn).Pointer(), name)
}

// RegisterSimple registers a function that does not return an error.
// The function is wrapped to return (U, nil) for compatibility with the registry.
func RegisterSimple[T, U any](name string, fn func(T) U) {
	Register(name, func(item T) (U, error) { return fn(item), nil })
}

// lookupName finds the registered name for fn by comparing function pointers.
// Returns the name and true if found, or ("", false) if the function was not registered.
func lookupName(fn any) (string, bool) {
	ptr := reflect.ValueOf(fn).Pointer()
	v, ok := reverseMap.Load(ptr)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// lookupEntry returns the registeredFn for the given name.
func lookupEntry(name string) (registeredFn, bool) {
	v, ok := registry.Load(name)
	if !ok {
		return registeredFn{}, false
	}
	return v.(registeredFn), true
}

// registeredFnName returns the registered name for fn, or an error if unregistered.
func registeredFnName(fn any) (string, error) {
	name, ok := lookupName(fn)
	if !ok {
		return "", fmt.Errorf("aji: function not registered; call aji.Register() before Map()")
	}
	return name, nil
}

// callRegistered invokes entry.fn on item via reflection.
// item must be assignable to entry.inputType.
func callRegistered(entry registeredFn, item any) (any, error) {
	fnVal := reflect.ValueOf(entry.fn)
	itemVal := reflect.ValueOf(item)
	if itemVal.Type() != entry.inputType {
		itemVal = itemVal.Convert(entry.inputType)
	}
	out := fnVal.Call([]reflect.Value{itemVal})
	// out[0] is U, out[1] is error
	if !out[1].IsNil() {
		return nil, out[1].Interface().(error)
	}
	return out[0].Interface(), nil
}
