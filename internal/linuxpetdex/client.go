package linuxpetdex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"codex-pets/internal/protocol"
)

type daemonClient struct {
	socketPath string
	timeout    time.Duration
	counter    uint64
}

func newDaemonClient(socketPath string, timeout time.Duration) *daemonClient {
	return &daemonClient{socketPath: socketPath, timeout: timeout}
}

func (c *daemonClient) request(ctx context.Context, method string, payload any, out any) error {
	if c.socketPath == "" {
		return fmt.Errorf("pet daemon socket is not configured")
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if c.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(c.timeout))
	}

	id := fmt.Sprintf("linux-petdex-%d", atomic.AddUint64(&c.counter, 1))
	message, err := protocol.NewRequest(id, method, payload)
	if err != nil {
		return err
	}
	data, err := protocol.EncodeLine(message)
	if err != nil {
		return err
	}
	if _, err := conn.Write(data); err != nil {
		return err
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return err
		}
		return fmt.Errorf("pet daemon closed the connection")
	}

	var response protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		return err
	}
	if response.Error != nil {
		return errors.New(response.Error.Message)
	}
	if out != nil && len(response.Payload) > 0 {
		if err := json.Unmarshal(response.Payload, out); err != nil {
			return err
		}
	}
	return nil
}
