package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/emersion/go-sasl"
)

// LoadConfig loads a configuration file (json encoded) and returns the relevant information.
// addr (hostname:port format) is the remote address for which to make a connection.
// folder_list (map[local_name]remote_name) is the list of folders for which to sync; default values are "inbox", "sent", and "archive".
// directory is the root directory containing the maildir
// mem represents the local representation of the mailbox
func LoadConfig(r io.Reader) (addr string, a sasl.Client, folder_list map[string]string, directory string, mem *Memory, e error) {
	userinfo := make(map[string]string)

	// load config from os.Stdin
	dec := json.NewDecoder(r)
	if e = dec.Decode(&userinfo); e != nil {
		return
	}
	directory = userinfo["directory"]
	os.MkdirAll(directory, os.ModePerm)

	// load memory file
	if m, err := MemoryInit(filepath.Join(directory, ".memory.json")); err != nil || m == nil || m.Boxes == nil {
		mem = &Memory{
			filename: filepath.Join(directory, ".memory.json"),
			Boxes:    make(map[string]MemoryMailbox),
		}
	} else {
		mem = m
	}
	addr = userinfo["imap_server"]
	switch userinfo["type"] {
	case "plain":
		a = sasl.NewPlainClient("", userinfo["user"], userinfo["password"])
		folder_list = make(map[string]string)
		folder_list["inbox"] = "INBOX"
		folder_list["sent"] = "sent"
		folder_list["archive"] = "archive"
	case "gmail":
		config, token := Gmail_Generate_Token(userinfo["clientid"], userinfo["clientsecret"], userinfo["refreshtoken"])
		a = XOAuth2(userinfo["user"], config, token)
		folder_list = make(map[string]string)
		folder_list["inbox"] = "INBOX"
		folder_list["sent"] = "[Gmail]/Sent Mail"
		// gmail had a strange archival system
		// does not work well with IMAP
	case "outlook":
		config, token := Outlook_Generate_Token(userinfo["clientid"], userinfo["refreshtoken"])
		a = XOAuth2(userinfo["user"], config, token)
		folder_list = make(map[string]string)
		folder_list["inbox"] = "INBOX"
		folder_list["sent"] = "Sent Items"
		folder_list["archive"] = "Archive"
	}
	return
}
