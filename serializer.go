package debug

import (
	"reflect"
	"strconv"

	"github.com/grafana/sobek"
)

const defaultMaxDepth = 3

// errorType is used to check if a sobek.Value implements the error interface.
var errorType = reflect.TypeFor[error]() //nolint:gochecknoglobals

// serializeValue converts a sobek.Value into a Go value suitable for json.Marshal.
// It handles primitives, arrays, objects, functions, errors, and ArrayBuffer.
// Circular references are detected and replaced with "[Circular]".
// Depth is limited to prevent huge payloads.
func serializeValue(rt *sobek.Runtime, v sobek.Value, maxDepth int) any {
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	return serialize(rt, v, maxDepth, 0, make(map[*sobek.Object]bool))
}

func serialize(rt *sobek.Runtime, v sobek.Value, maxDepth, depth int, seen map[*sobek.Object]bool) any {
	if v == nil || sobek.IsUndefined(v) {
		return nil
	}
	if sobek.IsNull(v) {
		return nil
	}

	if _, isFunc := sobek.AssertFunction(v); isFunc {
		return "[Function]"
	}

	if exportType := v.ExportType(); exportType != nil && exportType.Implements(errorType) {
		if exported := v.Export(); exported != nil {
			if err, ok := exported.(error); ok {
				return map[string]any{"error": err.Error()}
			}
		}
	}

	obj, isObj := v.(*sobek.Object)
	if !isObj {
		return v.Export()
	}

	// JS Error objects
	if obj.ClassName() == "Error" {
		return map[string]any{"error": obj.String()}
	}

	// ArrayBuffer
	if ab, ok := obj.Export().(sobek.ArrayBuffer); ok {
		return map[string]any{"type": "ArrayBuffer", "byteLength": len(ab.Bytes())}
	}

	if depth >= maxDepth {
		return "[MaxDepth]"
	}

	if seen[obj] {
		return "[Circular]"
	}
	seen[obj] = true
	defer delete(seen, obj)

	// Check if it's an array
	if isArray(rt, obj) {
		return serializeArray(rt, obj, maxDepth, depth, seen)
	}

	return serializeObject(rt, obj, maxDepth, depth, seen)
}

func isArray(rt *sobek.Runtime, obj *sobek.Object) bool {
	arrCtor := rt.Get("Array")
	if arrCtor == nil {
		return false
	}
	isArrayFn, ok := sobek.AssertFunction(arrCtor.ToObject(rt).Get("isArray"))
	if !ok {
		return false
	}
	result, err := isArrayFn(sobek.Undefined(), obj)
	if err != nil {
		return false
	}
	return result.ToBoolean()
}

func serializeArray(rt *sobek.Runtime, obj *sobek.Object, maxDepth, depth int, seen map[*sobek.Object]bool) any {
	length := int(obj.Get("length").ToInteger())
	result := make([]any, length)
	for i := 0; i < length; i++ {
		val := obj.Get(strconv.Itoa(i))
		result[i] = serialize(rt, val, maxDepth, depth+1, seen)
	}
	return result
}

func serializeObject(rt *sobek.Runtime, obj *sobek.Object, maxDepth, depth int, seen map[*sobek.Object]bool) any {
	result := make(map[string]any)
	for _, key := range obj.Keys() {
		val := obj.Get(key)
		result[key] = serialize(rt, val, maxDepth, depth+1, seen)
	}
	return result
}

// DAPVariable represents a variable for the DAP protocol.
type DAPVariable struct {
	Name               string
	Value              string
	Type               string
	VariablesReference int // >0 if expandable
}

// serializeForDAP converts a sobek.Value into a flat list of child properties
// with variablesReference IDs for expandable objects.
func serializeForDAP(rt *sobek.Runtime, v sobek.Value, refStore *VariableStore) []DAPVariable {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		return nil
	}

	obj, isObj := v.(*sobek.Object)
	if !isObj {
		return nil
	}

	var vars []DAPVariable
	for _, key := range obj.Keys() {
		child := obj.Get(key)
		dv := DAPVariable{
			Name:  key,
			Value: formatDAPValue(child),
			Type:  dapType(child),
		}

		// If the child is an expandable object/array, assign a reference
		if childObj, ok := child.(*sobek.Object); ok {
			if _, isFunc := sobek.AssertFunction(child); !isFunc {
				dv.VariablesReference = refStore.Add(childObj)
			}
		}
		vars = append(vars, dv)
	}
	return vars
}

func formatDAPValue(v sobek.Value) string {
	if v == nil || sobek.IsUndefined(v) {
		return "undefined"
	}
	if sobek.IsNull(v) {
		return "null"
	}
	if _, isFunc := sobek.AssertFunction(v); isFunc {
		return "[Function]"
	}
	return v.String()
}

func dapType(v sobek.Value) string {
	if v == nil || sobek.IsUndefined(v) {
		return "undefined"
	}
	if sobek.IsNull(v) {
		return "null"
	}
	if _, isFunc := sobek.AssertFunction(v); isFunc {
		return "function"
	}
	obj, isObj := v.(*sobek.Object)
	if !isObj {
		exported := v.Export()
		if exported == nil {
			return "undefined"
		}
		return reflect.TypeOf(exported).String()
	}
	return obj.ClassName()
}

// VariableStore manages DAP variablesReference IDs for expandable objects.
type VariableStore struct {
	refs   map[int]*sobek.Object
	nextID int
}

// NewVariableStore creates a new VariableStore.
func NewVariableStore() *VariableStore {
	return &VariableStore{
		refs:   make(map[int]*sobek.Object),
		nextID: 1,
	}
}

// Add stores a sobek object and returns a new reference ID.
func (vs *VariableStore) Add(obj *sobek.Object) int {
	id := vs.nextID
	vs.nextID++
	vs.refs[id] = obj
	return id
}

// Get returns the sobek object for a reference ID.
func (vs *VariableStore) Get(refID int) *sobek.Object {
	return vs.refs[refID]
}

// Clear resets the store between pauses.
func (vs *VariableStore) Clear() {
	vs.refs = make(map[int]*sobek.Object)
	vs.nextID = 1
}
