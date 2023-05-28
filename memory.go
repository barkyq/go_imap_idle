package main

import (
	"encoding/json"
	"os"
)

// handle memory... pretty basic idea
type MemoryMailbox struct {
	UidValidity *uint32           `json:"uid_validity"`
	Keys        map[uint32]string `json:"keys"`
}

type Memory struct {
	filename string
	Boxes    map[string]MemoryMailbox `json:"mailboxes"`
	// AccessToken string                   `json:"auth_token"`
	// Expiry      time.Time                `json:"expiry"`
}

type Dir string

func MemoryInit(filename string) (*Memory, error) {
	if f, e := os.Open(filename); e != nil {
		return nil, e
	} else {
		dec := json.NewDecoder(f)
		mem := &Memory{}
		e = dec.Decode(mem)
		mem.filename = filename
		f.Close()
		return mem, e
	}
}

func (mem *Memory) MemorySave() (err error) {
	if f, e := os.Create(mem.filename); e != nil {
		return e
	} else {
		enc := json.NewEncoder(f)
		enc.SetIndent("", " ")
		e = enc.Encode(mem)
		f.Close()
		return
	}
}
