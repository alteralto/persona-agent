package twitch

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		line string
		want Message
		ok   bool
	}{
		{":vasya!vasya@vasya.tmi.twitch.tv PRIVMSG #chan :привет, Пыхтун!", Message{"vasya", "привет, Пыхтун!"}, true},
		{"@badge-info=;color=#FF0000;display-name=Вася_Пупкин;mod=0 :vasya!vasya@vasya.tmi.twitch.tv PRIVMSG #chan :устрой салют",
			Message{"Вася_Пупкин", "устрой салют"}, true},
		{"@display-name= :petya!petya@petya.tmi.twitch.tv PRIVMSG #chan :hi", Message{"petya", "hi"}, true},
		{":petya!petya@petya.tmi.twitch.tv PRIVMSG #chan :\x01ACTION машет рукой\x01", Message{"petya", "машет рукой"}, true},
		{":tmi.twitch.tv 001 justinfan123 :Welcome, GLHF!", Message{}, false},
		{":justinfan123!justinfan123@justinfan123.tmi.twitch.tv JOIN #chan", Message{}, false},
		{"PING :tmi.twitch.tv", Message{}, false},
		{":vasya!vasya@vasya.tmi.twitch.tv PRIVMSG #chan :   ", Message{}, false},
	} {
		got, ok := Parse(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}

// A fake Twitch: checks the login, pings, sends a message, then drops the link once; the
// reader must answer the ping, pass the message on and come back.
func TestReaderSession(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	pong := make(chan string, 2)
	go func() {
		for i := 0; ; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn, i int) {
				defer conn.Close()
				br := bufio.NewReader(conn)
				var joined bool
				for !joined {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					joined = strings.HasPrefix(line, "JOIN #mychannel")
				}
				fmt.Fprintf(conn, "PING :tmi.twitch.tv\r\n")
				line, _ := br.ReadString('\n')
				pong <- strings.TrimSpace(line)
				fmt.Fprintf(conn, "@display-name=Зритель%d :viewer!viewer@viewer.tmi.twitch.tv PRIVMSG #mychannel :привет %d\r\n", i, i)
				if i == 0 {
					return // drop: the reader must reconnect
				}
				time.Sleep(5 * time.Second)
			}(conn, i)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := make(chan Message, 4)
	r := &Reader{Channel: "#MyChannel", Addr: ln.Addr().String()}
	go r.Run(ctx, out)
	for i := 0; i < 2; i++ {
		select {
		case p := <-pong:
			if p != "PONG :tmi.twitch.tv" {
				t.Errorf("pong = %q", p)
			}
		case <-ctx.Done():
			t.Fatal("no pong")
		}
		select {
		case m := <-out:
			if want := (Message{fmt.Sprintf("Зритель%d", i), fmt.Sprintf("привет %d", i)}); m != want {
				t.Errorf("message %d = %+v, want %+v", i, m, want)
			}
		case <-ctx.Done():
			t.Fatalf("no message %d", i)
		}
	}
}
