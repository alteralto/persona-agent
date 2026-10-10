// Package twitch reads a Twitch channel's chat, anonymously and read-only: Twitch lets any
// "justinfan" nick join a channel over IRC without a password or token, so nothing secret is
// kept. The stream's viewers can then talk to the bot.
package twitch

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"strings"
	"time"
)

// Message is one chat line from a viewer.
type Message struct {
	Nick string
	Text string
}

// Reader joins one channel and passes its chat lines on.
type Reader struct {
	Channel string // without '#'
	Addr    string // default irc.chat.twitch.tv:6697
	TLS     bool   // default true when Addr is empty
	Log     *slog.Logger
}

// Run reads until ctx ends, reconnecting with a growing pause (1 s .. 1 min) after a drop.
func (r *Reader) Run(ctx context.Context, out chan<- Message) {
	addr, useTLS := r.Addr, r.TLS
	if addr == "" {
		addr, useTLS = "irc.chat.twitch.tv:6697", true
	}
	channel := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.Channel), "#"))
	pause := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := r.session(ctx, addr, useTLS, channel, out)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			pause = time.Second // it ran fine for a while: a fresh drop
		}
		if r.Log != nil {
			r.Log.Warn("twitch chat dropped", "err", err, "retry_in", pause)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		pause = min(pause*2, time.Minute)
	}
}

func (r *Reader) session(ctx context.Context, addr string, useTLS bool, channel string, out chan<- Message) error {
	d := net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if useTLS {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: strings.Split(addr, ":")[0]})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { // close on cancel, which ends the read below
		<-ctx.Done()
		conn.Close()
	}()
	nick := fmt.Sprintf("justinfan%d", 10000+rand.IntN(89999))
	if _, err := fmt.Fprintf(conn, "CAP REQ :twitch.tv/tags\r\nNICK %s\r\nJOIN #%s\r\n", nick, channel); err != nil {
		return err
	}
	if r.Log != nil {
		r.Log.Info("twitch chat joined", "channel", channel)
	}
	br := bufio.NewReader(conn)
	for {
		// Twitch pings every ~5 minutes; silence for 7 means the link is dead.
		conn.SetReadDeadline(time.Now().Add(7 * time.Minute))
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("closed by server")
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if rest, ok := strings.CutPrefix(line, "PING"); ok {
			if _, err := fmt.Fprintf(conn, "PONG%s\r\n", rest); err != nil {
				return err
			}
			continue
		}
		if m, ok := Parse(line); ok {
			select {
			case out <- m:
			default: // the agent is busy: a viewer line can be dropped
			}
		}
		if strings.Contains(line, " RECONNECT") {
			return fmt.Errorf("server asked to reconnect")
		}
	}
}

// Parse reads a PRIVMSG line, with or without IRCv3 tags; the display name (with its case and
// Cyrillic) is preferred over the login.
func Parse(line string) (Message, bool) {
	tags := ""
	if strings.HasPrefix(line, "@") {
		var ok bool
		tags, line, ok = strings.Cut(line[1:], " ")
		if !ok {
			return Message{}, false
		}
	}
	if !strings.HasPrefix(line, ":") {
		return Message{}, false
	}
	prefix, rest, ok := strings.Cut(line[1:], " ")
	if !ok {
		return Message{}, false
	}
	cmd, rest, ok := strings.Cut(rest, " ")
	if !ok || cmd != "PRIVMSG" {
		return Message{}, false
	}
	_, text, ok := strings.Cut(rest, " :")
	if !ok {
		return Message{}, false
	}
	nick, _, _ := strings.Cut(prefix, "!")
	for _, kv := range strings.Split(tags, ";") {
		if v, ok := strings.CutPrefix(kv, "display-name="); ok && v != "" {
			nick = strings.ReplaceAll(v, `\s`, " ")
		}
	}
	text = strings.TrimSpace(text)
	// "/me waves" arrives as CTCP ACTION.
	if a, ok := strings.CutPrefix(text, "\x01ACTION "); ok {
		text = strings.TrimSuffix(a, "\x01")
	}
	if nick == "" || text == "" {
		return Message{}, false
	}
	return Message{Nick: nick, Text: text}, true
}
