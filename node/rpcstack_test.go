package node

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gochain/gochain/v5/rpc"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

func TestNewWebsocketUpgradeHandler_websocket(t *testing.T) {
	srv := rpc.NewServer()

	handler := NewWebsocketUpgradeHandler(nil, srv.WebsocketHandler([]string{"*"}))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	wsURL := "ws:" + strings.TrimPrefix(ts.URL, "http:")
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("could not issue a websocket request to the test server: %v", err)
	}
	defer conn.Close()
	assert.Equal(t, "websocket", response.Header.Get("Upgrade"))
}
