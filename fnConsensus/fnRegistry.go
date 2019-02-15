package fnConsensus

import (
	"errors"
	"sync"
)

var ErrFnIDIsTaken = errors.New("FnID is already used by another Fn Object")

type Fn interface {
	GetNonce() (int64, error)
	SubmitMultiSignedMessage(message []byte, signatures [][]byte)
	GetMessageAndSignature() ([]byte, []byte, error)
}

type FnRegistry interface {
	Get(fnID string) Fn
	Set(fnID string, fnObj Fn) error
}

// Transient registry, need to rebuild upon restart
type InMemoryFnRegistry struct {
	mtx   sync.RWMutex
	fnMap map[string]Fn
}

func NewInMemoryFnRegistry() *InMemoryFnRegistry {
	return &InMemoryFnRegistry{
		fnMap: make(map[string]Fn),
	}
}

func (f *InMemoryFnRegistry) Get(fnID string) Fn {
	f.mtx.RLock()
	defer f.mtx.RUnlock()
	return f.fnMap[fnID]
}

func (f *InMemoryFnRegistry) Set(fnID string, fnObj Fn) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()

	_, exists := f.fnMap[fnID]
	if exists {
		return ErrFnIDIsTaken
	}

	f.fnMap[fnID] = fnObj
	return nil
}
