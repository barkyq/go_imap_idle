package main

import (
	"bytes"
	"context"

	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	imap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	maildir "github.com/emersion/go-maildir"
	"github.com/emersion/go-sasl"

	"golang.org/x/oauth2"
)

type xOAuth2 struct {
	useremail string
	config    *oauth2.Config
	token     *oauth2.Token
}

func XOAuth2(useremail string, config *oauth2.Config, token *oauth2.Token) sasl.Client {
	return &xOAuth2{useremail, config, token}
}
func (a *xOAuth2) Start() (string, []byte, error) {
	tsrc := (a.config).TokenSource(context.Background(), a.token)
	if t, err := tsrc.Token(); err == nil {
		// fmt.Fprintf(os.Stderr, "%s token expires in %s\n", time.Now().Format("15:04:05"), time.Until(t.Expiry).String())
		str := fmt.Sprintf("user=%sauth=Bearer %s", a.useremail, t.AccessToken)
		resp := []byte(str)
		return "XOAUTH2", resp, nil
	} else {
		return "", []byte{}, err
	}
}
func (a *xOAuth2) Next(fromServer []byte) ([]byte, error) {
	return nil, fmt.Errorf("unexpected server challenge")
}

func main() {
	addr, a, folder_list, raw_archive_name, directory, mem, _, e := func() (addr string, a sasl.Client, folder_list map[string]string, raw_archive_name string, directory string, mem *Memory, timeout time.Duration, e error) {
		userinfo := make(map[string]string)

		// load config from os.Stdin
		dec := json.NewDecoder(os.Stdin)
		dec.Decode(&userinfo)
		directory = userinfo["directory"]
		os.MkdirAll(directory, os.ModePerm)

		// load memory file
		mem, err := MemoryInit(filepath.Join(directory, ".memory.json"))
		if err != nil || mem == nil || mem.Boxes == nil {
			mem = &Memory{
				Boxes: make(map[string]MemoryMailbox),
			}
		}
		addr = userinfo["imap_server"]
		switch userinfo["type"] {
		case "plain":
			// assume dovecot
			timeout = 24 * time.Hour
			a = sasl.NewPlainClient("", userinfo["user"], userinfo["password"])
			folder_list = make(map[string]string)
			folder_list["inbox"] = "INBOX"
			folder_list["sent"] = "sent"
			raw_archive_name = "archive"
		case "gmail":
			// see https://developers.google.com/gmail/imap/imap-smtp#session_length_limits
			timeout = 59 * time.Minute
			config := &oauth2.Config{
				ClientID:     userinfo["clientid"],
				ClientSecret: userinfo["clientsecret"],
				Endpoint: oauth2.Endpoint{
					AuthURL:   "https://accounts.google.com/o/oauth2/auth",
					TokenURL:  "https://oauth2.googleapis.com/token",
					AuthStyle: 0,
				},
				RedirectURL: "https://localhost",
				Scopes:      []string{"https://mail.google.com/"},
			}
			// the RefreshToken is really a *secret* piece of information
			// so do not share the source code!
			token := &oauth2.Token{
				TokenType:    "Bearer",
				RefreshToken: userinfo["refreshtoken"],
			}
			a = XOAuth2(userinfo["user"], config, token)
			folder_list = make(map[string]string)
			folder_list["inbox"] = "INBOX"
			folder_list["sent"] = "[Gmail]/Sent Mail"

			// gmail had a strange archival system
			// does not work well with IMAP
			raw_archive_name = ""
		case "outlook":
			timeout = time.Hour
			config := &oauth2.Config{
				ClientID: userinfo["clientid"],
				Endpoint: oauth2.Endpoint{
					AuthURL:   "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
					TokenURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/token",
					AuthStyle: oauth2.AuthStyleAutoDetect,
				},
				RedirectURL: "https://localhost:8080",
				Scopes:      []string{"offline_access", "https://outlook.office365.com/IMAP.AccessAsUser.All", "https://outlook.office365.com/SMTP.Send"},
			}
			// the RefreshToken is really a *secret* piece of information
			// so do not share the source code!
			token := &oauth2.Token{
				TokenType:    "Bearer",
				RefreshToken: userinfo["refreshtoken"],
			}
			a = XOAuth2(userinfo["user"], config, token)
			folder_list = make(map[string]string)
			folder_list["inbox"] = "INBOX"
			folder_list["sent"] = "Sent Items"

			// gmail had a strange archival system
			// does not work well with IMAP
			raw_archive_name = "Archive"
		}
		return
	}()
	if e != nil {
		panic(e)
	}
	socket_chan := make(chan struct{})
	go func() {
		sock_addr := filepath.Join(directory, ".socket")
		if e := os.RemoveAll(sock_addr); e != nil {
			panic(e)
		}
		l, e := net.Listen("unix", sock_addr)
		if e != nil {
			panic(e)
		}
		defer l.Close()
		var handler http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "signal received")
			socket_chan <- struct{}{}
		}
		for {
			if e := http.Serve(l, handler); e != nil {
				panic(e)
			}
		}
	}()

	// timeout_ch := time.Tick(timeout)
	memory_filename := filepath.Join(directory, ".memory.json")

	// capture Ctrl-C signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	for {
		// wrap so defer gets triggered
		// can exit by calling return
		func() {
			var kill_signal, timed_out bool
			c, e := client.DialTLS(addr, nil)
			if e != nil {
				panic(e)
			}
			defer c.Logout()
			// if _, y, e := a.Start(); e == nil {
			// 	fmt.Println(base64.StdEncoding.EncodeToString(y))
			// }
			if e := c.Authenticate(a); e != nil {
				panic(e)
			}
			for {
				if timed_out == true {
					return
				}
				for title, raw_title := range folder_list {
					if mem.Boxes[title].Keys == nil {
						box := MemoryMailbox{
							Keys: make(map[uint32]string),
						}
						mem.Boxes[title] = box
					}
					if mbox, e := c.Select(raw_title, false); e != nil {
						fmt.Fprintf(os.Stderr, "%s connection error!\n", time.Now().Format("15:04:05"))
						return
					} else if (mem.Boxes[title].UidValidity != nil) && (*mem.Boxes[title].UidValidity != mbox.UidValidity) {
						panic(fmt.Errorf("UIDValidity Mismatch!"))
					} else if mem.Boxes[title].UidValidity == nil {
						box := mem.Boxes[title]
						box.UidValidity = &mbox.UidValidity
						mem.Boxes[title] = box
					}
					D := maildir.Dir(filepath.Join(directory, title))
					if e := D.Init(); e != nil {
						panic(e)
					}

					// check keys compare to memory
					// uploading new items (usually for sent)
					not_to_delete := make(map[string]bool)
					if keys, e := D.Keys(); e == nil {
						new_uids := new(imap.SeqSet)
					OUTER:
						for _, key := range keys {
							for _, k := range mem.Boxes[title].Keys {
								if k == key {
									// key is in memory and also exists in directory
									not_to_delete[k] = true
									continue OUTER
								}
							}
							// key is not in memory
							var nuid uint32
							if new_uids.Empty() {
								fmt.Fprintf(os.Stderr, "uploading ")
							}
							// microsoft has a bug...
							if addr == "outlook.office365.com:993" {
								if mbox, e := c.Select(raw_title, false); e == nil {
									nuid = mbox.UidNext
								} else {
									panic(e)
								}
							} else {
								if stat, e := c.Status(raw_title, []imap.StatusItem{imap.StatusUidNext}); e == nil {
									nuid = stat.UidNext
								} else {
									panic(e)
								}
							}
							var date time.Time
							buf := bytes.NewBuffer(nil)
							if f, e := D.Open(key); e != nil {
								panic(e)
							} else {
								if msg, e := mail.ReadMessage(f); e != nil {
									panic(e)
								} else {
									date, e = msg.Header.Date()
									if e != nil {
										panic(e)
									}
									for _, h := range []string{
										"From",
										"To",
										"Subject",
										"In-Reply-To",
										"References",
										"Date",
										"Message-ID",
										"MIME-Version",
										"Content-Type",
										"Content-Disposition",
										"Content-Transfer-Encoding",
									} {
										if v := msg.Header.Get(h); v != "" {
											fmt.Fprintf(buf, "%s: %s\n", h, v)
										}
									}
									fmt.Fprintf(buf, "\n")
									io.Copy(buf, msg.Body)
								}
								f.Close()
							}
							var fl []string
							if tfl, e := D.Flags(key); e == nil {
								ttfl := deparseFlags(tfl)
								for _, i := range ttfl {
									fl = append(fl, i.(string))
								}
							} else {
								continue
							}
							if nukey, e := D.Copy(D, key); e == nil {
								mem.Boxes[title].Keys[nuid] = nukey
								not_to_delete[nukey] = true
								new_uids.AddNum(nuid)
							} else {
								panic(e)
							}
							if e := D.Remove(key); e != nil {
								panic(e)
							}
							if e := c.Append(raw_title, fl, date, buf); e != nil {
								panic(e)
							}
							if e := mem.MemorySave(memory_filename); e != nil {
								panic(e)
							}
						}
						if !new_uids.Empty() {
							fmt.Fprintf(os.Stderr, "%s to %s\n", new_uids.String(), title)
						}
					} else {
						panic(e)
					}
					// delete on remote
					delete_seq := new(imap.SeqSet)
					for uid, key := range mem.Boxes[title].Keys {
						if not_to_delete[key] == false {
							delete_seq.AddNum(uid)
							delete(mem.Boxes[title].Keys, uid)
						}
					}
					if !delete_seq.Empty() {
						item := imap.FormatFlagsOp(imap.AddFlags, true)
						flags := []interface{}{imap.DeletedFlag}
						fmt.Fprintf(os.Stderr, "deleting %s from remote %s\n", delete_seq.String(), title)
						c.UidStore(delete_seq, item, flags, nil)
						if err := c.Expunge(nil); err != nil {
							panic(err)
						}
					}

					section := &imap.BodySectionName{Peek: true}
					uid_items, fetch_items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags}, []imap.FetchItem{imap.FetchUid, imap.FetchFlags, section.FetchItem()}
					uid_chan, fetch_chan := make(chan *imap.Message, 10), make(chan *imap.Message, 10)
					uid_seq, fetch_seq := new(imap.SeqSet), new(imap.SeqSet)
					uid_done, fetch_done := make(chan error, 1), make(chan error, 1)
					if p, e := c.Status(raw_title, []imap.StatusItem{imap.StatusMessages}); e == nil {
						if p.Messages > 0 {
							uid_seq.AddRange(1, p.Messages)
						} else {
							// no messages to fetch
							var anything bool
							for uid, key := range mem.Boxes[title].Keys {
								anything = true
								delete(mem.Boxes[title].Keys, uid)
								if e := D.Remove(key); e != nil {
									panic(e)
								}
							}
							if anything {
								fmt.Fprintf(os.Stderr, "deleting all local messages from %s\n", title)
							}
							continue
						}
					} else {
						panic(e)
					}
					go func() {
						uid_done <- c.Fetch(uid_seq, uid_items, uid_chan)
					}()
					remote_uids := make(map[uint32]bool)
					for msg := range uid_chan {
						remote_uids[msg.Uid] = true
						if key, ok := mem.Boxes[title].Keys[msg.Uid]; ok == true {
							// have the message in memory. sync flags
							raw_flags := make([]maildir.Flag, 0)
							var length int = 0
							if cur_flags, e := D.Flags(key); e == nil {
								length = len(cur_flags)
								for _, fl := range cur_flags {
									raw_flags = append(raw_flags, fl)
								}
							}
							remote_flags := parseFlags(msg.Flags)
							for _, t := range remote_flags {
								raw_flags = append(raw_flags, t)
							}
							if len(raw_flags) == 0 {
								continue
							}
							flags := make([]maildir.Flag, 0)
							// to avoid repeats
							for _, f := range []maildir.Flag{maildir.FlagSeen, maildir.FlagReplied} {
								for _, a := range raw_flags {
									if a == f {
										flags = append(flags, f)
										break
									}
								}
							}
							if length < len(flags) {
								// some flags got added remote -> local
								// fmt.Println("R -> L", msg.SeqNum, key)
								if e := D.SetFlags(key, flags); e != nil {
									panic(e)
								}
							}
							if len(remote_flags) < len(flags) {
								// some flags got added local -> remote
								// fmt.Println("L -> R", msg.SeqNum, key)
								fflags := deparseFlags(flags)
								go func(seqnum uint32, out_flags []interface{}) {
									tmp := new(imap.SeqSet)
									tmp.AddNum(seqnum)
									if e := c.Store(tmp, imap.FormatFlagsOp(imap.AddFlags, true), out_flags, nil); e != nil {
										panic(e)
									}
								}(msg.SeqNum, fflags)
							}
							continue
						} else {
							// don't have in memory, need to fetch
							fetch_seq.AddNum(msg.Uid)
						}
					}
					if e := <-uid_done; e != nil {
						panic(e)
					}
					// delete the ones not in remote
					ldel_seq := new(imap.SeqSet)
					for uid, key := range mem.Boxes[title].Keys {
						if remote_uids[uid] == false {
							ldel_seq.AddNum(uid)
							if e := D.Remove(key); e == nil {
								delete(mem.Boxes[title].Keys, uid)
							}
						}
					}
					if !ldel_seq.Empty() {
						fmt.Fprintf(os.Stderr, "deleting %s from local %s\n", ldel_seq.String(), title)
					}
					if fetch_seq.Empty() {
						if e := mem.MemorySave(memory_filename); e != nil {
							panic(e)
						}
						continue
					}
					fmt.Fprintf(os.Stderr, "downloading %s to %s\n", fetch_seq.String(), title)
					go func() {
						fetch_done <- c.UidFetch(fetch_seq, fetch_items, fetch_chan)
					}()
					for msg := range fetch_chan {
						fl := parseFlags(msg.Flags)
						k, f, e := D.Create(fl)
						mem.Boxes[title].Keys[msg.Uid] = k
						if e != nil {
							panic(e)
						}
						if _, e := io.Copy(f, msg.GetBody(section)); e != nil {
							panic(e)
						}
						if e := f.Close(); e != nil {
							panic(e)
						}
					}
					if e := <-fetch_done; e != nil {
						panic(e)
					}
				}
				if e := mem.MemorySave(memory_filename); e != nil {
					panic(e)
				}

				// DL from archive
				if raw_archive_name != "" {
					D := maildir.Dir(filepath.Join(directory, "archive"))
					if e := D.Init(); e != nil {
						panic(e)
					}
					if mbox, e := c.Select(raw_archive_name, false); e != nil {
						panic(e)
					} else if mbox.Messages > 0 {
						fetch_seq := new(imap.SeqSet)

						fetch_seq.AddRange(1, mbox.Messages)
						section := &imap.BodySectionName{Peek: true}
						fetch_items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, section.FetchItem()}
						fetch_chan := make(chan *imap.Message, 10)
						done := make(chan error, 1)
						go func() {
							done <- c.Fetch(fetch_seq, fetch_items, fetch_chan)
						}()
						for msg := range fetch_chan {
							fl := parseFlags(msg.Flags)
							_, f, e := D.Create(fl)
							if e != nil {
								panic(e)
							}
							if _, e := io.Copy(f, msg.GetBody(section)); e != nil {
								panic(e)
							}
							if e := f.Close(); e != nil {
								panic(e)
							}
						}
						if e := <-done; e != nil {
							panic(e)
						} else {
							fmt.Fprintf(os.Stderr, "archiving %d from remote archive\n", mbox.Messages)
						}
						item := imap.FormatFlagsOp(imap.AddFlags, true)
						flags := []interface{}{imap.DeletedFlag}
						c.Store(fetch_seq, item, flags, nil)
						if err := c.Expunge(nil); err != nil {
							panic(err)
						}
					}
				}
				if _, e := c.Select("INBOX", false); e != nil {
					panic(e)
				}
				fmt.Fprintf(os.Stderr, "%s ... ", time.Now().Format("15:04:05"))
				updates := make(chan client.Update)
				done := make(chan error, 1)
				stop := make(chan struct{})
				c.Updates = updates
				var stopped bool
				go func() {
					done <- c.Idle(stop, nil)
				}()
			INNER:
				for {
					select {
					case upd := <-updates:
						if _, ok := upd.(*client.MailboxUpdate); ok == true {
							if !stopped {
								stopped = true
								close(stop)
							}
						}
						if _, ok := upd.(*client.ExpungeUpdate); ok == true {
							if !stopped {
								stopped = true
								close(stop)
							}
						}
					case <-c.LoggedOut():
						if !stopped {
							stopped = true
							close(stop)
						}
						timed_out = true
					case <-sigs:
						if !stopped {
							stopped = true
							close(stop)
						}
						kill_signal = true
					case <-socket_chan:
						if !stopped {
							stopped = true
							close(stop)
						}
					case <-done:
						c.Updates = nil
						if kill_signal == true {
							c.Logout()
							fmt.Fprintf(os.Stderr, "\nlog out\n")
							os.Exit(1)
						}
						if timed_out == true {
							fmt.Fprintf(os.Stderr, "%s (reauth)\n", time.Now().Format("15:04:05"))
						} else {
							fmt.Fprintf(os.Stderr, "%s\n", time.Now().Format("15:04:05"))
						}
						break INNER
					}
				}
			}
		}()
	}
}

// handle memory... pretty basic idea
type MemoryMailbox struct {
	UidValidity *uint32           `json:"uid_validity"`
	Keys        map[uint32]string `json:"keys"`
}

type Memory struct {
	Boxes map[string]MemoryMailbox `json:"mailboxes"`
	// AccessToken string                   `json:"auth_token"`
	// Expiry      time.Time                `json:"expiry"`
}

type Dir string

func MemoryInit(filename string) (mem *Memory, err error) {
	if f, e := os.Open(filename); e != nil {
		return nil, e
	} else {
		dec := json.NewDecoder(f)
		mem = &Memory{}
		e = dec.Decode(mem)
		f.Close()
		return
	}
}

func (mem *Memory) MemorySave(filename string) (err error) {
	if f, e := os.Create(filename); e != nil {
		return e
	} else {
		enc := json.NewEncoder(f)
		enc.SetIndent("", " ")
		e = enc.Encode(mem)
		f.Close()
		return
	}
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
