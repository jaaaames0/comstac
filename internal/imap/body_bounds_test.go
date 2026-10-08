package imap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"comstac/internal/mailcontent"
	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Exercise the real pinned IMAP decoder/adapter; body bytes are never allocated
// for either rejection case, including a server that lies about RFC822.SIZE.
func TestIMAPSizeAndLiteralBounds(t *testing.T) {
	for _, lie := range []bool{false, true} {
		t.Run(fmt.Sprintf("lying-size=%t", lie), func(t *testing.T) {
			client, server := net.Pipe()
			_ = server.SetDeadline(time.Now().Add(3 * time.Second))
			c := imapclient.New(client, nil)
			t.Cleanup(func() { c.Close(); server.Close() })
			done := make(chan error, 1)
			go func() {
				defer server.Close()
				if _, err := io.WriteString(server, "* PREAUTH [CAPABILITY IMAP4rev1] fixture\r\n"); err != nil {
					done <- err
					return
				}
				r := bufio.NewReader(server)
				line, err := r.ReadString('\n')
				if err != nil {
					done <- err
					return
				}
				tag := strings.Fields(line)[0]
				size := mailcontent.MaxMessageBytes + 1
				if lie {
					size = 10
				}
				if _, err = fmt.Fprintf(server, "* 1 FETCH (UID 101 RFC822.SIZE %d)\r\n%s OK metadata\r\n", size, tag); err != nil {
					done <- err
					return
				}
				if !lie {
					// No body fetch is allowed after oversized metadata.
					_, err = r.ReadString('\n')
					if err == nil {
						done <- errors.New("body fetch issued for oversized metadata")
						return
					}
					done <- nil
					return
				}
				line, err = r.ReadString('\n')
				if err != nil {
					done <- err
					return
				}
				if !strings.Contains(line, "BODY.PEEK[]<0.") {
					done <- fmt.Errorf("unbounded body request: %s", line)
					return
				}
				_, err = fmt.Fprintf(server, "* 1 FETCH (UID 101 BODY[]<0> {%d}\r\n", mailcontent.MaxMessageBytes+1)
				done <- err
			}()
			_, err := (clientMailbox{client: c, conn: client}).FetchMessages([]imaplib.UID{101})
			if !errors.Is(err, mailcontent.ErrTooLarge) {
				t.Fatalf("fetch error = %v", err)
			}
			c.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBodyFetchCancellationClosesBlockedDecoder(t *testing.T) {
	client, server := net.Pipe()
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	c := imapclient.New(client, nil)
	t.Cleanup(func() { c.Close(); server.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	requested := make(chan struct{})
	go func() {
		io.WriteString(server, "* PREAUTH [CAPABILITY IMAP4rev1] fixture\r\n")
		r := bufio.NewReader(server)
		line, _ := r.ReadString('\n')
		if len(strings.Fields(line)) == 0 {
			close(requested)
			return
		}
		fmt.Fprintf(server, "* 1 FETCH (UID 101 RFC822.SIZE 10)\r\n%s OK metadata\r\n", strings.Fields(line)[0])
		r.ReadString('\n')
		io.WriteString(server, "* 1 FETCH (UID 101 BODY[]<0> {10}\r\n")
		close(requested)
	}()
	done := make(chan error, 1)
	go func() {
		_, err := (clientMailbox{client: c, conn: client}).FetchMessages([]imaplib.UID{101})
		done <- err
	}()
	<-requested
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled fetch succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled fetch remained blocked")
	}
}
