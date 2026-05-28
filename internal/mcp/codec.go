package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

type Codec struct {
	r  *bufio.Reader
	w  io.Writer
	mu sync.Mutex
}

const MaxContentLength = 16 * 1024 * 1024

func NewCodec(r io.Reader, w io.Writer) *Codec {
	return &Codec{r: bufio.NewReader(r), w: w}
}

func (c *Codec) Read() (Message, error) {
	headers := map[string]string{}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return Message{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Message{}, fmt.Errorf("invalid header line %q", line)
		}
		headers[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}

	length, err := strconv.Atoi(headers["content-length"])
	if err != nil || length < 0 {
		return Message{}, fmt.Errorf("invalid content-length")
	}
	if length > MaxContentLength {
		return Message{}, fmt.Errorf("content-length %d exceeds maximum %d", length, MaxContentLength)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return Message{}, err
	}
	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

func (c *Codec) Write(msg Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)
	_, err = c.w.Write(buf.Bytes())
	return err
}
