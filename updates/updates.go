package updates

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jpillora/backoff"
	log "github.com/sirupsen/logrus"
)

// UpdateType is an enum of types of update between the proxy and migration services
type UpdateType int

// TableUpdate, Start, Complete, ... are the enums of the update types
const (
	TableUpdate = iota
	TableRestart
	Start
	Complete
	Shutdown
	Success
	Failure
)

// Update represents a request between the migration and proxy services
type Update struct {
	ID    string
	Type  UpdateType
	Data  []byte
	Error string
}

// New returns a new Update struct with a random UUID, the passed in Type and the passed in data.
func New(updateType UpdateType, data []byte) *Update {
	return &Update{
		ID:    uuid.New().String(),
		Type:  updateType,
		Data:  data,
		Error: "",
	}
}

// Success returns a serialized success response for the Update struct
func (u *Update) Success() ([]byte, error) {
	resp := Update{
		ID:   u.ID,
		Type: Success,
	}

	return resp.Serialize()
}

// Failure returns a serialized failure response for the Update struct
func (u *Update) Failure(err error) ([]byte, error) {
	resp := Update{
		ID:    u.ID,
		Type:  Failure,
		Error: err.Error(),
	}

	return resp.Serialize()
}

// Serialize appends the length of the message
func (u *Update) Serialize() ([]byte, error) {
	marshaled, err := json.Marshal(u)
	if err != nil {
		return nil, err
	}

	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(marshaled)))
	withLen := append(length, marshaled...)

	return withLen, nil
}

// Send sends an update to the connection
func Send(update *Update, dst net.Conn) error {
	marshaledUpdate, err := update.Serialize()
	if err != nil {
		return err
	}

	b := &backoff.Backoff{
		Min:    200 * time.Millisecond,
		Max:    10 * time.Second,
		Factor: 2,
		Jitter: false,
	}

	go func() {
		for {
			_, err = dst.Write(marshaledUpdate)
			if err != nil {
				duration := b.Duration()
				log.Errorf("Unable to send update %v to %s. Retrying in %s...", update, dst.RemoteAddr(),
					duration.String())
				time.Sleep(duration)
			} else {
				log.Debugf("SENT: %v", string(marshaledUpdate))
				return
			}
		}
	}()

	return nil
}

// CommunicationHandler is used to handle incoming updates
func CommunicationHandler(src net.Conn, dst net.Conn, handler func(update *Update) error) {
	defer src.Close()

	length := make([]byte, 4)
	for {
		bytesRead, err := src.Read(length)
		if err != nil {
			if err == io.EOF {
				log.Error(err)
				os.Exit(100)
				return
			}
		}

		if bytesRead < 4 {
			log.Error("Received updated does not have full update length header")
			continue
		}

		updateLen := binary.BigEndian.Uint32(length)
		buf := make([]byte, updateLen)
		bytesRead, err = src.Read(buf)
		if uint32(bytesRead) < updateLen {
			log.Error("Received update has missing bytes")
			continue
		}

		log.Debug("RECEIVED: " + string(buf))
		var update Update
		err = json.Unmarshal(buf, &update)
		if err != nil {
			log.WithError(err).Error("Error unmarshalling received")
			continue
		}

		handlerErr := handler(&update)
		if update.Type != Success && update.Type != Failure {
			var resp []byte
			if handlerErr != nil {
				resp, err = update.Failure(err)
			} else {
				resp, err = update.Success()
			}

			if err != nil {
				log.WithError(err).Error("Error creating success/failure response")
			}

			_, err = dst.Write(resp)
			if err != nil {
				log.WithError(err).Error("Error sending success/failure response")
			}
		}
	}
}
