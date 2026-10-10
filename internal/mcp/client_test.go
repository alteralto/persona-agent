package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// fakeMod answers tools/call getGameState with one player's name.
func fakeMod(name string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := fmt.Sprintf(`{\"self\":{\"name\":\"%s\"}}`, name)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"%s"}]}}`, state)
	}))
}

func port(t *testing.T, s *httptest.Server) int {
	_, p, _ := net.SplitHostPort(s.Listener.Addr().String())
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestForPlayerFindsItsClient(t *testing.T) {
	other, mine := fakeMod("arca_pro"), fakeMod("arcmlchikkk")
	defer other.Close()
	c := ForPlayer("ArcMlChikkk", "tok", 0)
	c.ports = []int{port(t, other), port(t, mine)}
	out, err := c.Call(context.Background(), "getGameState", nil)
	if err != nil || out != `{"self":{"name":"arcmlchikkk"}}` {
		t.Fatalf("Call = %q, %v; want the bot's own client", out, err)
	}
	// Its client goes away (a game restart): the next call fails, then it looks again.
	mine.Close()
	if _, err := c.Call(context.Background(), "getGameState", nil); err == nil {
		t.Fatal("a call to a closed client succeeded")
	}
	if c.found != "" {
		t.Error("kept the url of a client that stopped answering")
	}
	c.lookedAt = c.lookedAt.Add(-3 * time.Second)
	if _, err := c.Call(context.Background(), "getGameState", nil); err == nil {
		t.Error("took another player's client")
	}
}
