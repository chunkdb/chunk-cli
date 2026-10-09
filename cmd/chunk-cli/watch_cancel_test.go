package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func watchWireBulk(text string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(text), text) }
func watchWireMap(pairs ...string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%%%d\r\n", len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out.WriteString(watchWireBulk(pairs[i]))
		out.WriteString(pairs[i+1])
	}
	return out.String()
}

func TestWatchCancellationDuringSchemaHandshakeDrainsOnly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hello := watchWireMap("protocol", ":3\r\n", "server_version", watchWireBulk("2.0"), "max_line_bytes", ":1048576\r\n", "max_parameters", ":1024\r\n", "max_area_chunks", ":4096\r\n", "max_response_bytes", ":67108864\r\n", "max_scan_limit", ":4096\r\n", "server_signature", "_\r\n")
	column := watchWireMap("id", ":1\r\n", "name", watchWireBulk("v"), "type", watchWireBulk("u8"), "null", "#f\r\n", "required", "#f\r\n", "default", "_\r\n")
	describe := watchWireMap("table", watchWireBulk("world"), "version", ":1\r\n", "chunk", "*2\r\n:2\r\n:2\r\n", "large", "*2\r\n:4\r\n:4\r\n", "options", "%0\r\n", "columns", "*1\r\n"+column)
	change := func(version int) string {
		return ">7\r\n" + watchWireBulk("change") + watchWireBulk(watchEpoch) + fmt.Sprintf(":2\r\n:123\r\n_\r\n:%d\r\n*1\r\n*4\r\n:0\r\n:0\r\n_\r\n*1\r\n:7\r\n", version)
	}
	var count atomic.Int32
	var mu sync.Mutex
	var sockets []net.Conn
	var workers sync.WaitGroup
	blocked := make(chan struct{})
	unwatched := make(chan struct{})
	serverErrors := make(chan error, 8)
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			socket, err := listener.Accept()
			if err != nil {
				return
			}
			id := count.Add(1)
			mu.Lock()
			sockets = append(sockets, socket)
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer socket.Close()
				r := bufio.NewReader(socket)
				line, err := r.ReadString('\n')
				if err != nil || line != "HELLO 3\r\n" {
					serverErrors <- fmt.Errorf("HELLO: %q %v", line, err)
					return
				}
				if id == 3 {
					close(blocked)
					_, _ = io.Copy(io.Discard, r)
					return
				}
				if _, err := io.WriteString(socket, hello); err != nil {
					serverErrors <- err
					return
				}
				line, err = r.ReadString('\n')
				if err != nil {
					serverErrors <- err
					return
				}
				if id == 1 {
					if line != "DESCRIBE world\r\n" {
						serverErrors <- fmt.Errorf("DESCRIBE: %q", line)
						return
					}
					_, _ = io.WriteString(socket, describe)
					_, _ = io.Copy(io.Discard, r)
					return
				}
				if line != "WATCH world\r\n" {
					serverErrors <- fmt.Errorf("WATCH: %q", line)
					return
				}
				_, _ = io.WriteString(socket, "+OK "+watchEpoch+" 0\r\n"+change(2))
				line, err = r.ReadString('\n')
				if err != nil || line != "UNWATCH\r\n" {
					serverErrors <- fmt.Errorf("UNWATCH: %q %v", line, err)
					return
				}
				close(unwatched)
				_, _ = io.WriteString(socket, change(3)+"+OK\r\n")
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for _, socket := range sockets {
			socket.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var output bytes.Buffer
	result := make(chan error, 1)
	go func() {
		result <- runWatch(ctx, globalOptions{URI: "chunk://" + listener.Addr().String() + "/", Timeout: time.Hour}, watchOptions{table: "world", statement: "WATCH world"}, console{}, &output)
	}()
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("schema HELLO was not reached")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation waited for the hour-long timeout")
	}
	select {
	case <-unwatched:
	default:
		t.Fatal("UNWATCH not sent")
	}
	if count.Load() != 3 || output.String() != "start "+watchEpoch+":0\n" {
		t.Fatalf("decoded while draining: %d connections, %q", count.Load(), output.String())
	}
	select {
	case err := <-serverErrors:
		t.Fatal(err)
	default:
	}
}
