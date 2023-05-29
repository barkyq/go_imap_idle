package main

import (
	"fmt"

	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	imap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	maildir "github.com/emersion/go-maildir"
)

func main() {
	addr, a, folder_list, directory, mem, e := LoadConfig(os.Stdin)
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
					D := maildir.Dir(filepath.Join(directory, title))
					if e := D.Init(); e != nil {
						panic(e)
					}

					var mbox *imap.MailboxStatus
					if m, e := c.Select(raw_title, false); e != nil {
						fmt.Fprintf(os.Stderr, "%s connection error!\n", time.Now().Format("15:04:05"))
						return
					} else {
						switch title {
						case "archive":
							if m.Messages > 0 {
								if e := ArchiveHandler(c, D, m); e != nil {
									panic(e)
								}
							}
							continue
						default:
							mbox = m
						}
					}
					if mem.Boxes[title].Keys == nil {
						box := MemoryMailbox{
							Keys: make(map[uint32]string),
						}
						mem.Boxes[title] = box
					} else if (mem.Boxes[title].UidValidity != nil) && (*mem.Boxes[title].UidValidity != mbox.UidValidity) {
						panic(fmt.Errorf("UIDValidity Mismatch!"))
					} else if mem.Boxes[title].UidValidity == nil {
						box := mem.Boxes[title]
						box.UidValidity = &mbox.UidValidity
						mem.Boxes[title] = box
					}
					// check keys compare to memory
					// uploading new items (usually for sent)
					if mb, ok := mem.Boxes[title]; ok {
						// for k, v := range mb.Keys {
						// 	fmt.Println(k, v)
						// }
						// os.Exit(0)
						if addr == "outlook.office365.com:993" {
							if e := UploadHandler(c, D, mbox, &mb, true); e != nil {
								panic(e)
							}
						} else {
							if e := UploadHandler(c, D, mbox, &mb, false); e != nil {
								panic(e)
							}
						}
						if e := DownloadHandler(c, D, mbox, &mb); e != nil {
							panic(e)
						}
					} else {
						panic(ok)
					}
					if e := mem.MemorySave(); e != nil {
						panic(e)
					}
				}

				// IDLE loop
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
