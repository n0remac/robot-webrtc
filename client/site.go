package client

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type SiteClient struct {
	URL        string
	Token      string
	Controller *Controller
	Camera     *CameraProcess
}

// Run keeps the robot connected to the website until the context is canceled.
func (c *SiteClient) Run(ctx context.Context) error {
	if c.Controller == nil || c.Camera == nil {
		return fmt.Errorf("site client requires a controller and camera")
	}
	endpoint, err := robotWebSocketURL(c.URL)
	if err != nil {
		return err
	}

	backoff := time.Second
	for ctx.Err() == nil {
		err := c.connect(ctx, endpoint)
		c.Controller.StopAll()
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("website connection lost: %v; retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func (c *SiteClient) connect(ctx context.Context, endpoint string) error {
	header := http.Header{}
	if c.Token != "" {
		header.Set("Authorization", "Bearer "+c.Token)
	}
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, endpoint, header)
	if err != nil {
		if response != nil {
			return fmt.Errorf("connect to website: %s", response.Status)
		}
		return fmt.Errorf("connect to website: %w", err)
	}
	defer conn.Close()
	log.Printf("connected to robot website at %s", endpoint)

	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errors := make(chan error, 2)
	go func() {
		<-connectionCtx.Done()
		_ = conn.Close()
	}()
	go c.writeFrames(connectionCtx, conn, errors)
	go c.readControls(connectionCtx, conn, errors)

	select {
	case <-ctx.Done():
		return nil
	case err := <-errors:
		return err
	}
}

func (c *SiteClient) writeFrames(ctx context.Context, conn *websocket.Conn, errors chan<- error) {
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()
	var sent uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			frame, seq := c.Camera.LatestFrame()
			if len(frame) == 0 || seq == sent {
				continue
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				errors <- fmt.Errorf("send camera frame: %w", err)
				return
			}
			sent = seq
		}
	}
}

func (c *SiteClient) readControls(ctx context.Context, conn *websocket.Conn, errors chan<- error) {
	conn.SetReadLimit(1024)
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				errors <- fmt.Errorf("read control message: %w", err)
			}
			return
		}
		if messageType != websocket.TextMessage {
			continue
		}
		if err := c.Controller.Handle(data); err != nil {
			log.Printf("ignored control message: %v", err)
		}
	}
}

func robotWebSocketURL(siteURL string) (string, error) {
	if strings.TrimSpace(siteURL) == "" {
		return "", fmt.Errorf("website URL is required")
	}
	u, err := url.Parse(siteURL)
	if err != nil {
		return "", fmt.Errorf("parse website URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("website URL must use http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("website URL must include a host")
	}
	u.Path = path.Join(u.Path, "/ws/robot")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
