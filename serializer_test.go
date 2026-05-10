package debug

import (
	"encoding/json"
	"testing"

	"github.com/grafana/sobek"
)

func TestSerializePrimitives(t *testing.T) {
	t.Parallel()
	rt := sobek.New()

	tests := []struct {
		name string
		val  sobek.Value
		want any
	}{
		{"nil", nil, nil},
		{"undefined", sobek.Undefined(), nil},
		{"null", sobek.Null(), nil},
		{"int", rt.ToValue(42), int64(42)},
		{"float", rt.ToValue(3.14), 3.14},
		{"string", rt.ToValue("hello"), "hello"},
		{"bool_true", rt.ToValue(true), true},
		{"bool_false", rt.ToValue(false), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := serializeValue(rt, tc.val, 3)
			// Compare via JSON to handle type differences
			got, _ := json.Marshal(result)
			want, _ := json.Marshal(tc.want)
			if string(got) != string(want) {
				t.Errorf("got %s, want %s", got, want)
			}
		})
	}
}

func TestSerializeFunction(t *testing.T) {
	t.Parallel()
	rt := sobek.New()
	fn, err := rt.RunString("(function foo() {})")
	if err != nil {
		t.Fatal(err)
	}
	result := serializeValue(rt, fn, 3)
	if result != "[Function]" {
		t.Errorf("got %v, want [Function]", result)
	}
}

func TestSerializeObject(t *testing.T) {
	t.Parallel()
	rt := sobek.New()
	val, err := rt.RunString(`({a: 1, b: "two", c: true})`)
	if err != nil {
		t.Fatal(err)
	}
	result := serializeValue(rt, val, 3)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result)
	}
	if m["a"] != int64(1) {
		t.Errorf("a: got %v (%T)", m["a"], m["a"])
	}
	if m["b"] != "two" {
		t.Errorf("b: got %v", m["b"])
	}
	if m["c"] != true {
		t.Errorf("c: got %v", m["c"])
	}
}

func TestSerializeArray(t *testing.T) {
	t.Parallel()
	rt := sobek.New()
	val, err := rt.RunString(`[1, "two", 3]`)
	if err != nil {
		t.Fatal(err)
	}
	result := serializeValue(rt, val, 3)
	arr, ok := result.([]any)
	if !ok {
		t.Fatalf("expected array, got %T", result)
	}
	if len(arr) != 3 {
		t.Errorf("len: got %d, want 3", len(arr))
	}
}

func TestSerializeNestedObject(t *testing.T) {
	t.Parallel()
	rt := sobek.New()
	val, err := rt.RunString(`({a: {b: {c: {d: "deep"}}}})`)
	if err != nil {
		t.Fatal(err)
	}
	result := serializeValue(rt, val, 3)
	// At depth 3, the innermost object should be [MaxDepth]
	data, _ := json.Marshal(result)
	var m map[string]any
	json.Unmarshal(data, &m)

	a := m["a"].(map[string]any)
	b := a["b"].(map[string]any)
	c := b["c"]
	if c != "[MaxDepth]" {
		t.Errorf("expected [MaxDepth] at depth 3, got %v", c)
	}
}

func TestSerializeCircularRef(t *testing.T) {
	t.Parallel()
	rt := sobek.New()
	_, err := rt.RunString(`var obj = {}; obj.self = obj;`)
	if err != nil {
		t.Fatal(err)
	}
	val := rt.Get("obj")
	result := serializeValue(rt, val, 3)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result)
	}
	if m["self"] != "[Circular]" {
		t.Errorf("expected [Circular], got %v", m["self"])
	}
}

func TestSerializeError(t *testing.T) {
	t.Parallel()
	rt := sobek.New()
	val, err := rt.RunString(`new Error("test error")`)
	if err != nil {
		t.Fatal(err)
	}
	result := serializeValue(rt, val, 3)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result)
	}
	if m["error"] != "Error: test error" {
		t.Errorf("got %v", m["error"])
	}
}

func TestVariableStore(t *testing.T) {
	t.Parallel()
	vs := NewVariableStore()

	rt := sobek.New()
	obj := rt.NewObject()

	id1 := vs.Add(obj)
	if id1 != 1 {
		t.Errorf("first ID should be 1, got %d", id1)
	}

	id2 := vs.Add(obj)
	if id2 != 2 {
		t.Errorf("second ID should be 2, got %d", id2)
	}

	if vs.Get(id1) != obj {
		t.Error("Get(1) returned wrong object")
	}

	vs.Clear()
	if vs.Get(id1) != nil {
		t.Error("Get after Clear should return nil")
	}
}
