package bifrost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
)

const testManagementToken = "mgmt-token-test"

// mockBifrost is an httptest-based stand-in for Bifrost's management API,
// implementing just the virtual-key endpoints the engine uses (docs/08). It
// supports fault injection to exercise error, WAL-rollback, and retry paths.
type mockBifrost struct {
	server *httptest.Server

	mu   sync.Mutex
	keys map[string]*mockVK // id -> key
	seq  int64

	// Fault injection knobs.
	failCreateStatus int          // if non-zero, create returns this status
	failDeleteStatus int          // if non-zero, delete returns this status
	requireAuth      bool         // if true, wrong bearer token yields 401
	createCount      atomic.Int64 // observed create calls
	deleteCount      atomic.Int64 // observed delete calls
}

type mockVK struct {
	ID    string
	Name  string
	Value string
}

func newMockBifrost() *mockBifrost {
	m := &mockBifrost{keys: map[string]*mockVK{}, requireAuth: true}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *mockBifrost) Close() { m.server.Close() }

func (m *mockBifrost) addr() string { return m.server.URL }

// countKeysNamed returns how many stored keys have the given name.
func (m *mockBifrost) countKeysNamed(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, k := range m.keys {
		if k.Name == name {
			n++
		}
	}
	return n
}

func (m *mockBifrost) handle(w http.ResponseWriter, r *http.Request) {
	if m.requireAuth && r.Header.Get("Authorization") != "Bearer "+testManagementToken {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	const base = "/api/governance/virtual-keys"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base:
		m.handleCreate(w, r)
	case r.Method == http.MethodGet && r.URL.Path == base:
		m.handleList(w)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
		m.handleDelete(w, strings.TrimPrefix(r.URL.Path, base+"/"))
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, base+"/"):
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"/"):
		m.handleGet(w, strings.TrimPrefix(r.URL.Path, base+"/"))
	default:
		http.NotFound(w, r)
	}
}

func (m *mockBifrost) handleCreate(w http.ResponseWriter, r *http.Request) {
	m.createCount.Add(1)
	if m.failCreateStatus != 0 {
		http.Error(w, `{"error":"injected"}`, m.failCreateStatus)
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	m.mu.Lock()
	m.seq++
	id := "vk_" + itoa(m.seq)
	value := "sk-bf-" + id
	m.keys[id] = &mockVK{ID: id, Name: body.Name, Value: value}
	m.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Virtual key created successfully",
		"virtual_key": map[string]interface{}{
			"id":    id,
			"name":  body.Name,
			"value": value,
		},
	})
}

func (m *mockBifrost) handleList(w http.ResponseWriter) {
	m.mu.Lock()
	out := make([]map[string]interface{}, 0, len(m.keys))
	for _, k := range m.keys {
		out = append(out, map[string]interface{}{"id": k.ID, "name": k.Name, "value": k.Value})
	}
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"virtual_keys": out})
}

func (m *mockBifrost) handleGet(w http.ResponseWriter, id string) {
	m.mu.Lock()
	k, ok := m.keys[id]
	m.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"virtual_key": map[string]interface{}{"id": k.ID, "name": k.Name, "value": k.Value},
	})
}

func (m *mockBifrost) handleDelete(w http.ResponseWriter, id string) {
	m.deleteCount.Add(1)
	if m.failDeleteStatus != 0 {
		http.Error(w, `{"error":"injected"}`, m.failDeleteStatus)
		return
	}
	m.mu.Lock()
	_, ok := m.keys[id]
	delete(m.keys, id)
	m.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
