package main

import (
	"bytes"
	"fmt"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-maildir"
	"io"
	"net/mail"
	"os"
	"sync"
	"time"
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
	for msg := range fetch_chan {
		fl := parseFlags(msg.Flags)
		k, f, e := D.Create(fl)
		mem.Keys[msg.Uid] = k
		if e != nil {
			return e
		}
		if _, e := io.Copy(f, msg.GetBody(section)); e != nil {
			return e
		}
		if e := f.Close(); e != nil {
			return e
		}
	}
	return <-fetch_done
}

func UploadHandler(c *client.Client, D maildir.Dir, mbox *imap.MailboxStatus, mem *MemoryMailbox, microsoftp bool) error {
	not_to_delete := make(map[string]bool)
	if keys, e := D.Keys(); e == nil {
		new_uids := new(imap.SeqSet)
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
			if new_uids.Empty() {
				fmt.Fprintf(os.Stderr, "uploading %s", mbox.Name)
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
			buf := bytes.NewBuffer(nil)
			if f, e := D.Open(key); e != nil {
				return e
			} else {
				if msg, e := mail.ReadMessage(f); e != nil {
					return e
				} else {
					date, e = msg.Header.Date()
					if e != nil {
						return e
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
				mem.Keys[nuid] = nukey
				not_to_delete[nukey] = true
				new_uids.AddNum(nuid)
			} else {
				return e
			}
			if e := D.Remove(key); e != nil {
				return e
			}
			if e := c.Append(mbox.Name, fl, date, buf); e != nil {
				return e
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

// ArchiveHandler just downloads everything remote -> local and delete on remote.
func ArchiveHandler(c *client.Client, D maildir.Dir, mbox *imap.MailboxStatus) error {
	if e := D.Init(); e != nil {
		return e
	}
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
			return e
		}
		if _, e := io.Copy(f, msg.GetBody(section)); e != nil {
			return e
		}
		if e := f.Close(); e != nil {
			return e
		}
	}
	if e := <-done; e != nil {
		return e
	} else {
		fmt.Fprintf(os.Stderr, "archiving %d from remote archive\n", mbox.Messages)
	}
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.DeletedFlag}
	c.Store(fetch_seq, item, flags, nil)
	if err := c.Expunge(nil); err != nil {
		return err
	}

	return nil
}
