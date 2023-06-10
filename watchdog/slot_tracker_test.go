package watchdog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jumpcrypto/mirasol/common/ws"
	"github.com/stretchr/testify/require"
)

var upgrader = websocket.Upgrader{}

// serve mimics the following Solana PubSub methods:
// - slotSubscribe
// - slotNotification
// - slotUnsubscribe
//
// After slotSubscribe is received, slotNotification messages will be sent
// periodically (short interval) until either:
// - slotUnsubscribe is received, or
// - context is cancelled.
func serve(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Wait for slotSubscribe message.
	if err := waitSlotSubscribe(conn); err != nil {
		panic(err)
	}

	slotNotificationFormat := `
	{
		"jsonrpc": "2.0",
		"method": %q,
		"params": {
		  "result": {
			"parent": 75,
			"root": 44,
			"slot": %d
		  },
		  "subscription": 0
		}
	  }	
	`

	// Periodically write a new slot notification.
	//
	// Make this non-blocking so we can also wait for slotUnsubscribe. This
	// opens up the possibility of having concurrent write operations, one
	// writes slot notification while the other writes the response to
	// slotUnsubscribe.
	mutexWrite := &sync.RWMutex{}
	go func(ctx context.Context) {
		tick := time.NewTicker(time.Millisecond * 500)
		defer tick.Stop()
		slotHeight := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				slotHeight++
				message := fmt.Sprintf(slotNotificationFormat, ws.MethodSlotNotification, slotHeight)
				mutexWrite.Lock()
				err = conn.WriteMessage(websocket.TextMessage, []byte(message))
				// MUST NOT defer Unlock as Lock is called on every tick.
				mutexWrite.Unlock()
				if err != nil {
					panic(err)
				}
			}
		}
	}(r.Context())

	// Wait for unsubscription message.
	if err := waitSlotUnsubscribe(conn, mutexWrite); err != nil {
		panic(err)
	}
}

func waitSlotSubscribe(conn *websocket.Conn) error {
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var req *ws.Request
		err = json.Unmarshal(message, &req)
		if err != nil {
			return err
		}
		if req.Method == ws.MethodSlotSubscribe {
			respFormat := `
			{
				"jsonrpc": "2.0",
				"result": 0,
				"id": %d
			}`
			resp := fmt.Sprintf(respFormat, req.ID)
			if err := conn.WriteMessage(websocket.TextMessage, []byte(resp)); err != nil {
				return err
			}
			break
		}
	}

	return nil
}

func waitSlotUnsubscribe(conn *websocket.Conn, mutexWrite *sync.RWMutex) error {
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var req *ws.Request
		err = json.Unmarshal(message, &req)
		if err != nil {
			return err
		}
		if req.Method == ws.MethodSlotUnsubscribe {
			respFormat := `
		{
			"jsonrpc": "2.0",
			"result": true,
			"id": %d
		}`
			resp := fmt.Sprintf(respFormat, req.ID)
			// If mutexWrite is nil, it means no locking is necessary.
			if mutexWrite != nil {
				mutexWrite.Lock()
				defer mutexWrite.Unlock()
			}
			err := conn.WriteMessage(websocket.TextMessage, []byte(resp))
			if err != nil {
				return err
			}
			break
		}
	}

	return nil
}

func Test_Unit_Track(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(serve))
	defer s.Close()

	// Remove scheme.
	urlWithoutScheme := strings.TrimPrefix(s.URL, "http://")
	t.Logf("Websocket server listening at %q", urlWithoutScheme)
	addrPubSub, err := netip.ParseAddrPort(urlWithoutScheme)
	if err != nil {
		t.Errorf("invalid IP:port pair: %s", err.Error())
	}

	// Track.
	whatever := newTracker("whatever", addrPubSub)
	board := newSlotBoard(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	err = whatever.Track(ctx, board)

	// Assert results.
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.GreaterOrEqual(t, board.HighestSlot(), uint64(0), "board should have higher slot number")

	t.Logf("stopping Websocket server listening at %q", urlWithoutScheme)
}

// serveNoSlotNotifications mimics the following Solana PubSub methods:
// - slotSubscribe
// - slotUnsubscribe
//
// After slotSubscribe is received and accepted, no slotSubscriptions are sent.
// The client may handle this or not.
// This function exists when either:
// - slotUnsubscribe is received, or
// - context is cancelled.
func serveNoSlotNotifications(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Wait for slotSubscribe message.
	if err := waitSlotSubscribe(conn); err != nil {
		panic(err)
	}

	// Wait for unsubscription message.
	if err := waitSlotUnsubscribe(conn, nil); err != nil {
		panic(err)
	}
}

func Test_Unit_Track_hearbeat_timeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(serveNoSlotNotifications))
	defer s.Close()

	// Remove scheme.
	urlWithoutScheme := strings.TrimPrefix(s.URL, "http://")
	t.Logf("Websocket server listening at %q", urlWithoutScheme)
	addrPubSub, err := netip.ParseAddrPort(urlWithoutScheme)
	if err != nil {
		t.Errorf("invalid IP:port pair: %s", err.Error())
	}

	// Track.
	whatever := newTracker("whatever", addrPubSub)
	board := newSlotBoard(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	err = whatever.Track(ctx, board)

	// Assert results.
	require.ErrorIs(t, err, errNoSlotNotificationsWithinTimeWindow)
	require.Zero(t, board.HighestSlot())

	t.Logf("stopping Websocket server listening at %q", urlWithoutScheme)
}
