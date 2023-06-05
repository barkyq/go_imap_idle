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
			fmt.Fprintf(w, "%s: %s\n", h, v)
		}
	}
	w.Write([]byte{'\n'})
	for {
		if b, e := rb.ReadSlice('\r'); e == io.EOF {
			if k, e := w.Write(b); e != nil {
				n += k
				return n, e
			}
			break
		} else if e != nil {
			return n, e
		} else {
			if k, e := w.Write(b[:len(b)-1]); e != nil {
				n += k
				return n, e
			}
		}
		if t, e := rb.Peek(1); e != nil {
			return n, e
		} else if t[0] == '\n' {
			w.Write([]byte{'\n'})
			rb.Discard(1)
		} else {
			w.Write([]byte{'\r'})
		}
	}
	return n, nil
}
