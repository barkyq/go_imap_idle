package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-maildir"
)

func DownloadHandler(c *client.Client, D maildir.Dir, mbox *imap.MailboxStatus, mem *MemoryMailbox) error {
	section := &imap.BodySectionName{Peek: true}
	uid_items, fetch_items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags}, []imap.FetchItem{imap.FetchUid, imap.FetchFlags, section.FetchItem()}
	uid_chan, fetch_chan := make(chan *imap.Message, 10), make(chan *imap.Message, 10)
	uid_seq, fetch_seq := new(imap.SeqSet), new(imap.SeqSet)
	uid_done, fetch_done := make(chan error, 1), make(chan error, 1)
	if p, e := c.Status(mbox.Name, []imap.StatusItem{imap.StatusMessages}); e == nil {
		if p.Messages > 0 {
			uid_seq.AddRange(1, p.Messages)
		} else {
			// no messages to fetch
			var anything bool
			for uid, key := range mem.Keys {
				anything = true
				delete(mem.Keys, uid)
				if e := D.Remove(key); e != nil {
					return e
				}
			}
			if anything {
				fmt.Fprintf(os.Stderr, "deleting all local messages from %s\n", mbox.Name)
			}
			return nil
		}
	} else {
		return e
	}
	go func() {
		uid_done <- c.Fetch(uid_seq, uid_items, uid_chan)
	}()
	remote_uids := make(map[uint32]bool)

	// sync flags
	fchan := make(chan *FlagUpdateRequest)
	var wg sync.WaitGroup
	go func() {
		for f := range fchan {
			tmp := new(imap.SeqSet)
			tmp.AddNum(f.uid)
			if e := c.Store(tmp, imap.FormatFlagsOp(imap.AddFlags, true), f.flags, nil); e != nil {
				panic(e)
			}
		}
	}()
	for msg := range uid_chan {
		remote_uids[msg.Uid] = true
		if key, ok := mem.Keys[msg.Uid]; ok == true {
			// have the message in memory. sync flags
			if f, e := SyncFlags(key, D, msg.Flags); e != nil && f != nil {
				wg.Add(1)
				go func(seqnum uint32, f []interface{}) {
					defer wg.Done()
					// send a FlagUpdateRequest
					fchan <- &FlagUpdateRequest{seqnum, f}
				}(msg.SeqNum, f)
			}
		} else {
			// don't have in memory, need to fetch
			fetch_seq.AddNum(msg.Uid)
		}
	}
	if e := <-uid_done; e != nil {
		return e
	}
	wg.Wait()
	close(fchan)

	// delete the ones not in remote
	ldel_seq := new(imap.SeqSet)
	for uid, key := range mem.Keys {
		if remote_uids[uid] == false {
			ldel_seq.AddNum(uid)
			if e := D.Remove(key); e == nil {
				delete(mem.Keys, uid)
			}
		}
	}
	if !ldel_seq.Empty() {
		fmt.Fprintf(os.Stderr, "deleting %s from local %s\n", ldel_seq.String(), mbox.Name)
	}
	if fetch_seq.Empty() {
		return nil
	}
	fmt.Fprintf(os.Stderr, "downloading %s to local %s\n", fetch_seq.String(), mbox.Name)
	go func() {
		fetch_done <- c.UidFetch(fetch_seq, fetch_items, fetch_chan)
	}()

	buffer := new(bufio.Reader)
	for msg := range fetch_chan {
		fl := parseFlags(msg.Flags)
		k, f, e := D.Create(fl)
		mem.Keys[msg.Uid] = k
		if e != nil {
			return e
		}

		if msg, e := mail.ReadMessage(msg.GetBody(section)); e != nil {
			return e
		} else {
			if _, e = msg.Header.Date(); e != nil {
				return e
			}
			buffer.Reset(msg.Body)
			if _, e := WriteMessage(msg.Header, buffer, f); e != nil {
				panic(e)
			}
		}
		if e := f.Close(); e != nil {
			return e
		}
	}
	return <-fetch_done
}

func UploadHandler(c *client.Client, D maildir.Dir, mbox *imap.MailboxStatus, mem *MemoryMailbox, microsoftp bool, limit int64) error {
	rb := new(bufio.Reader)
	not_to_delete := make(map[string]bool)
	if keys, e := D.Keys(); e == nil {
		new_uids := new(imap.SeqSet)
		var first bool = true
	OUTER:
		for _, key := range keys {
			for _, k := range mem.Keys {
				if k == key {
					// key is in memory and also exists in directory
					not_to_delete[k] = true
					continue OUTER
				}
			}
			// key is not in memory
			var nuid uint32
			if first {
				fmt.Fprintf(os.Stderr, "uploading to %s ", mbox.Name)
				first = false
			}
			// microsoft has a bug...
			if microsoftp {
				if m, e := c.Select(mbox.Name, false); e == nil {
					nuid = m.UidNext
					mbox = m
				} else {
					return e
				}
			} else {
				if stat, e := c.Status(mbox.Name, []imap.StatusItem{imap.StatusUidNext}); e == nil {
					nuid = stat.UidNext
				} else {
					return e
				}
			}
			var date time.Time
			var toobig bool
			buf := bytes.NewBuffer(nil)
			if s, e := D.Filename(key); e != nil {
				return e
			} else if info, e := os.Stat(s); e != nil {
				return e
			} else if info.Size() > limit {
				toobig = true
			} else if f, e := D.Open(key); e != nil {
				return e
			} else {
				if msg, e := mail.ReadMessage(f); e != nil {
					return e
				} else {
					date, e = msg.Header.Date()
					if e != nil {
						return e
					}
					rb.Reset(msg.Body)
					if _, e := WriteMessage(msg.Header, rb, buf); e != nil {
						panic(e)
					}
				}
				f.Close()
			}
			if toobig {
				if s, e := D.Filename(key); e != nil {
					return e
				} else if e := save_to_archive(filepath.Join(filepath.Dir(string(D)), "offline"), buf, s); e != nil {
					return e
				}
			} else {
				var fl []string
				if tfl, e := D.Flags(key); e == nil {
					ttfl := deparseFlags(tfl)
					for _, i := range ttfl {
						fl = append(fl, i.(string))
					}
					if nukey, e := D.Copy(D, key); e == nil {
						mem.Keys[nuid] = nukey
						not_to_delete[nukey] = true
						new_uids.AddNum(nuid)
					} else {
						return e
					}
				} else if nukey, w, e := D.Create(nil); e == nil {
					mem.Keys[nuid] = nukey
					not_to_delete[nukey] = true
					new_uids.AddNum(nuid)
					if _, e := w.Write(buf.Bytes()); e != nil {
						return e
					}
				}
				if e := c.Append(mbox.Name, fl, date, buf); e != nil {
					return e
				}
				if e := D.Remove(key); e != nil {
					return e
				}
			}
		}
		if !new_uids.Empty() {
			fmt.Fprintf(os.Stderr, "%s to %s\n", new_uids.String(), mbox.Name)
		}
	} else {
		return e
	}
	// delete on remote
	delete_seq := new(imap.SeqSet)
	for uid, key := range mem.Keys {
		if not_to_delete[key] == false {
			delete_seq.AddNum(uid)
			delete(mem.Keys, uid)
		}
	}
	if !delete_seq.Empty() {
		item := imap.FormatFlagsOp(imap.AddFlags, true)
		flags := []interface{}{imap.DeletedFlag}
		fmt.Fprintf(os.Stderr, "deleting %s from remote %s\n", delete_seq.String(), mbox.Name)
		c.UidStore(delete_seq, item, flags, nil)
		if err := c.Expunge(nil); err != nil {
			panic(err)
		}
	}
	return nil
}
