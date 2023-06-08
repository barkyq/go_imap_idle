package main

import (
	"bufio"
	"fmt"
	"io"
	"net/mail"
)

var header_list = []string{
	"From",
	"To",
	"Cc",
	"Subject",
	"In-Reply-To",
	"References",
	"Date",
	"Message-ID",
	"MIME-Version",
	"Content-Type",
	"Content-Disposition",
	"Content-Transfer-Encoding",
}

func WriteMessage(headers mail.Header, rb *bufio.Reader, w io.Writer) (n int, e error) {
	for _, h := range header_list {
		if v := headers.Get(h); v != "" {
			if k, e := fmt.Fprintf(w, "%s: %s\n", h, v); e != nil {
				return n + k, e
			} else {
				n += k
			}
		}
	}
	w.Write([]byte{'\n'})
	n++
	for {
		if b, isPrefix, e := rb.ReadLine(); e == io.EOF || isPrefix {
			if k, e := w.Write(b); e != nil {
				return n + k, e
			} else {
				n += k
			}
			if e == io.EOF {

				break
			} else {
				continue
			}
		} else if e != nil {
			panic(e)
		} else if !isPrefix {
			if k, e := w.Write(b); e != nil {
				return n + k, e
			} else {
				w.Write([]byte{'\n'})
				n += k + 1
			}
		} else {
			return n, fmt.Errorf("error writing message")
		}
	}
	return n, nil
}
