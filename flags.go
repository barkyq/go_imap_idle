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
