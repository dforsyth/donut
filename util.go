package donut

import (
	"encoding/json"
	"fmt"
	"launchpad.net/gozk/zookeeper"
	"log"
	"path"
	"sync"
)

// A locked map
type SafeMap struct {
	_map map[string]interface{}
	lk   sync.RWMutex
}

// Create a new SafeMap
func NewSafeMap(initial map[string]interface{}) *SafeMap {
	m := &SafeMap{
		_map: make(map[string]interface{}),
	}
	for key, val := range initial {
		m._map[key] = val
	}
	return m
}

// Get a value from the map
func (m *SafeMap) Get(key string) interface{} {
	m.lk.RLock()
	defer m.lk.RUnlock()
	return m._map[key]
}

// Check whether a key exists in the map
func (m *SafeMap) Contains(key string) bool {
	m.lk.RLock()
	defer m.lk.RUnlock()
	_, ok := m._map[key]
	return ok
}

// Remove a key from the map
func (m *SafeMap) Delete(key string) interface{} {
	m.lk.Lock()
	defer m.lk.Unlock()
	v := m._map[key]
	delete(m._map, key)
	return v
}

// Put a key, value into the map
func (m *SafeMap) Put(key string, value interface{}) interface{} {
	m.lk.Lock()
	defer m.lk.Unlock()
	old := m._map[key]
	m._map[key] = value
	return old
}

// Take an extended lock over the map
func (m *SafeMap) RangeLock() map[string]interface{} {
	m.lk.RLock()
	return m._map
}

// Release extended lock
func (m *SafeMap) RangeUnlock() {
	m.lk.RUnlock()
}

// Copy the map into a normal map
func (m *SafeMap) GetCopy() map[string]interface{} {
	m.lk.RLock()
	defer m.lk.RUnlock()
	_m := make(map[string]interface{})
	for k, v := range m._map {
		_m[k] = v
	}
	return _m
}

// Clear th map
func (m *SafeMap) Clear() {
	m.lk.Lock()
	defer m.lk.Unlock()
	m._map = make(map[string]interface{})
}

// Get the size of the map
func (m *SafeMap) Len() int {
	m.lk.RLock()
	defer m.lk.RUnlock()
	return len(m._map)
}

// Dump the map as a string
func (m *SafeMap) Dump() string {
	m.lk.RLock()
	defer m.lk.RUnlock()
	rval := ""
	for k, v := range m._map {
		rval += fmt.Sprintf("(key: %s, value: %v)\n", k, v)
	}
	return rval
}

// List all keys in the map
func (m *SafeMap) Keys() (keys []string) {
	_m := m.RangeLock()
	defer m.RangeUnlock()
	for k := range _m {
		keys = append(keys, k)
	}
	return
}

// Watch the children at path until a byte is sent on the returned channel
// Uses the SafeMap more like a set, so you'll have to use Contains() for entries
func watchZKChildren(zk *zookeeper.Conn, path string, children *SafeMap, onChange func(*SafeMap)) (chan byte, error) {
	initial, _, watch, err := zk.ChildrenW(path)
	if err != nil {
		return nil, err
	}
	m := children.RangeLock()
	for _, node := range initial {
		m[node] = nil
	}
	children.RangeUnlock()
	kill := make(chan byte, 1)
	go func() {
		defer close(kill)
		var nodes []string
		var err error
		for {
			select {
			case <-kill:
				// close(watch)
				return
			case event := <-watch:
				if !event.Ok() {
					continue
				}
				// close(watch)
				nodes, _, watch, err = zk.ChildrenW(path)
				if err != nil {
					log.Printf("Error in watchZkChildren: %v", err)
					// XXX I should really provide some way for the client to find out about this error...
					return
				}
				m := children.RangeLock()
				// mark all dead
				for k := range m {
					m[k] = 0
				}
				for _, node := range nodes {
					m[node] = 1
				}
				for k, v := range m {
					if v.(int) == 0 {
						delete(m, k)
					}
				}
				children.RangeUnlock()
				onChange(children)
			}
		}
	}()
	log.Printf("watcher setup on %s", path)
	return kill, nil
}

func serializeCreate(zk *zookeeper.Conn, path string, data map[string]interface{}) (err error) {
	var e []byte
	if e, err = json.Marshal(data); err != nil {
		return
	}
	_, err = zk.Create(path, string(e), 0, zookeeper.WorldACL(zookeeper.PERM_ALL))
	return
}

func getDeserialize(zk *zookeeper.Conn, path string) (data map[string]interface{}, err error) {
	var e string
	e, _, err = zk.Get(path)
	if err != nil {
		log.Printf("error on get in getDeserialize for %s: %v", path, err)
		return
	}
	err = json.Unmarshal([]byte(e), &data)
	return
}

// Create work in a cluster
func CreateWork(clusterName string, zk *zookeeper.Conn, config *Config, workId string, data map[string]interface{}) (err error) {
	p := path.Join("/", clusterName, config.WorkPath, workId)
	if err = serializeCreate(zk, p, data); err != nil {
		log.Printf("Failed to create work %s (%s): %v", workId, p, err)
	} else {
		log.Printf("Created work %s", p)
	}
	return
}

// Remove work from a cluster
func CompleteWork(clusterName string, zk *zookeeper.Conn, config *Config, workId string) {
	p := path.Join("/", clusterName, config.WorkPath, workId)
	zk.Delete(p, -1)
	log.Printf("Deleted work %s (%s)", workId, p)
}
