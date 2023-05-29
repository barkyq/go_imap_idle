package main

import (
	"github.com/emersion/go-imap"
	"github.com/emersion/go-maildir"
)

type FlagUpdateRequest struct {
	uid   uint32
	flags []interface{}
}

// flag handler
func parseFlags(in_flags []string) (out_flags []maildir.Flag) {
	for _, f := range in_flags {
		switch f {
		case "\\Seen":
			out_flags = append(out_flags, maildir.FlagSeen)
		case "\\Answered":
			out_flags = append(out_flags, maildir.FlagReplied)
		}
	}
	return
}
func deparseFlags(in_flags []maildir.Flag) (out_flags []interface{}) {
	for _, f := range in_flags {
		switch f {
		case maildir.FlagSeen:
			out_flags = append(out_flags, imap.SeenFlag)
		case maildir.FlagReplied:
			out_flags = append(out_flags, imap.AnsweredFlag)
		}
	}
	return
}

func SyncFlags(key string, D maildir.Dir, raw_remote_flags []string) ([]interface{}, error) {
	raw_flags := make([]maildir.Flag, 0)
	var length int = 0
	// get local flags
	if cur_flags, e := D.Flags(key); e == nil {
		length = len(cur_flags)
		for _, fl := range cur_flags {
			raw_flags = append(raw_flags, fl)
		}
	}
	// get remote flags
	remote_flags := parseFlags(raw_remote_flags)
	for _, t := range remote_flags {
		raw_flags = append(raw_flags, t)
	}
	if len(raw_flags) == 0 {
		return nil, nil
	}

	local_and_global_flags := make([]maildir.Flag, 0)
	// delete repeats
	for _, f := range []maildir.Flag{maildir.FlagSeen, maildir.FlagReplied} {
		for _, a := range raw_flags {
			if a == f {
				local_and_global_flags = append(local_and_global_flags, f)
				break
			}
		}
	}

	if length < len(local_and_global_flags) {
		// some flags got added remote -> local
		// fmt.Println("R -> L", msg.SeqNum, key)
		if e := D.SetFlags(key, local_and_global_flags); e != nil {
			return nil, e
		}
	}

	if len(remote_flags) < len(local_and_global_flags) {
		// some flags got added local -> remote
		// fmt.Println("L -> R", msg.SeqNum, key)
		return deparseFlags(local_and_global_flags), nil
	}

	return nil, nil
}
