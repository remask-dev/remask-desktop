package sse

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

const defaultMaxEventBytes = 1 << 20

type Event struct {
	Event string
	Data  []string
	ID    string
	Retry *int
}

func (e Event) DataString() string { return strings.Join(e.Data, "\n") }

func (e *Event) SetData(value string) { e.Data = strings.Split(value, "\n") }

type Decoder struct {
	reader        *bufio.Reader
	maxEventBytes int
}

func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{reader: bufio.NewReader(reader), maxEventBytes: defaultMaxEventBytes}
}

func (d *Decoder) Decode() (Event, error) {
	var event Event
	size := 0
	for {
		line, err := d.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			if errors.Is(err, io.EOF) && (event.Event != "" || len(event.Data) > 0 || event.ID != "" || event.Retry != nil) {
				return event, nil
			}
			return Event{}, err
		}
		size += len(line)
		if size > d.maxEventBytes {
			return Event{}, errors.New("SSE event exceeds size limit")
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			return event, nil
		}
		if strings.HasPrefix(line, ":") {
			if err != nil {
				return event, nil
			}
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field, value = line, ""
		} else if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			event.Event = value
		case "data":
			event.Data = append(event.Data, value)
		case "id":
			event.ID = value
		case "retry":
			if milliseconds, parseErr := strconv.Atoi(value); parseErr == nil && milliseconds >= 0 {
				event.Retry = &milliseconds
			}
		}
		if err != nil {
			return event, nil
		}
	}
}

func Encode(event Event) []byte {
	var output bytes.Buffer
	if event.Event != "" {
		output.WriteString("event: ")
		output.WriteString(event.Event)
		output.WriteByte('\n')
	}
	if event.ID != "" {
		output.WriteString("id: ")
		output.WriteString(event.ID)
		output.WriteByte('\n')
	}
	if event.Retry != nil {
		output.WriteString("retry: ")
		output.WriteString(strconv.Itoa(*event.Retry))
		output.WriteByte('\n')
	}
	for _, data := range event.Data {
		for _, line := range strings.Split(data, "\n") {
			output.WriteString("data: ")
			output.WriteString(line)
			output.WriteByte('\n')
		}
	}
	output.WriteByte('\n')
	return output.Bytes()
}
