package chunkclient

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestWatchUnwatchDrainsPushesAndAcknowledgement(t *testing.T) {
	socket, peer := net.Pipe()
	defer socket.Close()
	defer peer.Close()
	client := &Client{conn: socket, reader: bufio.NewReader(socket), writer: bufio.NewWriter(socket), timeout: time.Second}
	done := make(chan error, 1)
	go func() {
		r := bufio.NewReader(peer)
		line, err := r.ReadString('\n')
		if err != nil {
			done <- err
			return
		}
		if line != "WATCH world\r\n" {
			done <- fmt.Errorf("got %q", line)
			return
		}
		_, err = io.WriteString(peer, "+OK 0123456789abcdef0123456789abcdef 1\r\n")
		if err != nil {
			done <- err
			return
		}
		line, err = r.ReadString('\n')
		if err != nil || line != "UNWATCH\r\n" {
			done <- fmt.Errorf("got %q: %v", line, err)
			return
		}
		_, err = io.WriteString(peer, ">3\r\n$6\r\nresync\r\n$32\r\n0123456789abcdef0123456789abcdef\r\n:2\r\n+OK\r\n")
		done <- err
	}()
	if _, err := client.BeginWatch("WATCH world"); err != nil {
		t.Fatal(err)
	}
	if err := client.EndWatch(); err != nil {
		t.Fatal(err)
	}
	push, err := client.ReadWatch()
	if err != nil || push.Kind != KindPush || len(push.Items) != 3 {
		t.Fatalf("push %+v %v", push, err)
	}
	ack, err := client.ReadWatch()
	if err != nil || ack.Kind != KindSimple || ack.Text != "OK" {
		t.Fatalf("ack %+v %v", ack, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWatchRejectsNestedPush(t *testing.T) {
	if _, err := readReply(bufio.NewReader(strings.NewReader("*1\r\n>0\r\n"))); err == nil {
		t.Fatal("accepted nested push")
	}
}

func TestWatchReadConnectionLoss(t *testing.T) {
	socket, peer := net.Pipe()
	defer socket.Close()
	peer.Close()
	client := &Client{conn: socket, reader: bufio.NewReader(socket)}
	if _, err := client.ReadWatch(); err == nil {
		t.Fatal("connection loss succeeded")
	}
}
