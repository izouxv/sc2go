package sc2

import (
	"fmt"
	"net/url"
	"time"

	sc2api "github.com/Bredgren/sc2go/sc2apiprotocol"
	"github.com/golang/protobuf/proto"
	"golang.org/x/net/websocket"
)

// RequestID is used to match a request to its response.
type RequestID int

// Client wraps a websocket.Conn so that it can easily be used directly as such while also
// wrapping the SC2 API protocol.
type Client struct {
	*websocket.Conn
	status       sc2api.Status
	port         int
	responseBuf  map[RequestID]*sc2api.Response
	lastRequest  RequestID
	lastResponse RequestID
}

// NewClient connects to an SC2 client instance at the given port.
func NewClient(port int) (*Client, error) {
	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", port), Path: "/sc2api"}

	origin := "http://localhost/"
	var conn *websocket.Conn
	var err error
	for secondsToTry := timeout; secondsToTry > 0; secondsToTry-- {
		conn, err = websocket.Dial(u.String(), "", origin)
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if conn == nil {
		return nil, fmt.Errorf("timed out connecting to SC2")
	}

	cl := &Client{
		Conn:        conn,
		status:      sc2api.Status_launched,
		port:        port,
		responseBuf: make(map[RequestID]*sc2api.Response),
	}
	return cl, nil
}

// GetStatus returns the current state of the client.
func (c *Client) GetStatus() sc2api.Status {
	return c.status
}

// GetPort returns the port used to connect to SC2.
func (c *Client) GetPort() int {
	return c.port
}

// WaitForClose blocks until the application is closed for any reason.
func (c *Client) WaitForClose() {
	for {
		_, err := c.Ping()
		if err != nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
}

// WaitForEnd blocks until the game state is ended or closed.
func (c *Client) WaitForEnd() {
	for {
		_, err := c.Ping()
		if err != nil {
			break
		}
		if c.GetStatus() == sc2api.Status_ended {
			break
		}
		time.Sleep(1 * time.Second)
	}
}

// Request initiates an API request and returns a RequestID for retrieving the response.
// Multiple requests can be issued before getting the responses.
func (c *Client) Request(req *sc2api.Request) (RequestID, error) {
	data, err := proto.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("marshal: %v", err)
	}

	if _, err = c.Write(data); err != nil {
		return 0, fmt.Errorf("write: %v", err)
	}

	c.lastRequest++
	return c.lastRequest, nil
}

// Response retrieves the response for the given RequestID. The response for any given
// RequestID can only be retrieved once. Responses for outstanding requests can be retrieved
// in any order and if the requested response is not yet ready then this function will
// block until it is.
func (c *Client) Response(id RequestID) (*sc2api.Response, error) {
	for c.lastResponse < id {
		c.lastResponse++
		resp := &sc2api.Response{}
		err := c.nextResponse(resp)
		if err != nil {
			return nil, err
		}
		c.responseBuf[c.lastResponse] = resp
	}
	resp, ok := c.responseBuf[id]
	if !ok {
		return nil, fmt.Errorf("no response for ID %d, did you already retrieve it?", id)
	}
	delete(c.responseBuf, id)
	return resp, nil
}

// ReqResp does a request then blocks until the response is received and returns it.
func (c *Client) ReqResp(req *sc2api.Request) (*sc2api.Response, error) {
	id, err := c.Request(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.Response(id)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) nextResponse(resp *sc2api.Response) error {
	var msg []byte
	var buf = make([]byte, 1024)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return fmt.Errorf("read: %v", err)
		}
		msg = append(msg, buf[:n]...)
		if n < len(buf) {
			break
		}
	}

	err := proto.Unmarshal(msg, resp)
	if err != nil {
		return fmt.Errorf("unmarshal: %v", err)
	}

	c.status = resp.Status
	return nil
}

// Quit closes the SC2 client application.
func (c *Client) Quit() {
	req := &sc2api.Request{
		Request: &sc2api.Request_Quit{
			Quit: &sc2api.RequestQuit{},
		},
	}
	c.Request(req)
}

// GetAvailableMaps returns maps available to play on.
func (c *Client) GetAvailableMaps() (*sc2api.ResponseAvailableMaps, error) {
	req := &sc2api.Request{
		Request: &sc2api.Request_AvailableMaps{
			AvailableMaps: &sc2api.RequestAvailableMaps{},
		},
	}
	resp, err := c.ReqResp(req)
	if err != nil {
		return nil, err
	}

	return resp.GetAvailableMaps(), nil
}

// Ping executes a ping request and returns the response.
func (c *Client) Ping() (*sc2api.ResponsePing, error) {
	req := &sc2api.Request{
		Request: &sc2api.Request_Ping{
			Ping: &sc2api.RequestPing{},
		},
	}
	resp, err := c.ReqResp(req)
	if err != nil {
		return nil, err
	}

	return resp.GetPing(), nil
}

// CreateGame creates a new game with the given settings.
func (c *Client) CreateGame(settings *sc2api.RequestCreateGame) error {
	req := &sc2api.Request{
		Request: &sc2api.Request_CreateGame{
			CreateGame: settings,
		},
	}
	resp, err := c.ReqResp(req)
	if err != nil {
		return err
	}
	cg := resp.GetCreateGame()
	if cg.Error != 0 {
		return fmt.Errorf("create game: %s (%s)", cg.GetError(), cg.GetErrorDetails())
	}
	return nil
}

// JoinGame joins the game and returns your player id.
func (c *Client) JoinGame(settings *sc2api.RequestJoinGame) (playerID uint32, e error) {
	id, err := c.JoinGameRequest(settings)
	if err != nil {
		return 0, err
	}
	return c.JoinGameResponse(id)
}

// JoinGameRequest does just the request part of joining a game.
func (c *Client) JoinGameRequest(settings *sc2api.RequestJoinGame) (RequestID, error) {
	req := &sc2api.Request{
		Request: &sc2api.Request_JoinGame{
			JoinGame: settings,
		},
	}
	return c.Request(req)
}

// JoinGameResponse waits until joining was successful and returns the player id.
func (c *Client) JoinGameResponse(id RequestID) (playerID uint32, e error) {
	resp, err := c.Response(id)
	if err != nil {
		return 0, err
	}
	jg := resp.GetJoinGame()
	if jg.Error != 0 {
		return 0, fmt.Errorf("join game: %s (%s)", jg.GetError(), jg.GetErrorDetails())
	}
	return jg.GetPlayerId(), nil
}
