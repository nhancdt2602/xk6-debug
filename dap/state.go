package dap

import (
	"sync"

	"github.com/grafana/sobek"
)

// VariableRef manages DAP variablesReference IDs for expandable objects.
// DAP uses integer reference IDs so the IDE can request children of objects.
type VariableRef struct {
	mu     sync.Mutex
	refs   map[int]*sobek.Object
	nextID int
}

// NewVariableRef creates a new VariableRef store.
func NewVariableRef() *VariableRef {
	return &VariableRef{
		refs:   make(map[int]*sobek.Object),
		nextID: 1,
	}
}

// Add stores a sobek object and returns a new reference ID.
func (vr *VariableRef) Add(obj *sobek.Object) int {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	id := vr.nextID
	vr.nextID++
	vr.refs[id] = obj
	return id
}

// Get returns the sobek object for a reference ID.
func (vr *VariableRef) Get(refID int) *sobek.Object {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	return vr.refs[refID]
}

// Clear resets the store between pause points.
func (vr *VariableRef) Clear() {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	vr.refs = make(map[int]*sobek.Object)
	vr.nextID = 1
}
