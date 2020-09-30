package cloudgateproxy

import (
	"context"
	"errors"
	"github.com/riptano/cloud-gate/proxy/pkg/metrics"
	log "github.com/sirupsen/logrus"
	"io"
	"net"
	"sync"
)

/*
  This owns:
    - a response channel to send responses back to the client
    - the actual TCP connection
*/

type ClientConnector struct {

	// connection to the client
	connection net.Conn

	// channel on which the ClientConnector sends requests as it receives them from the client
	requestChannel chan *Frame
	// channel on which the ClientConnector listens for responses to send to the client
	responseChannel chan []byte

	lock           *sync.RWMutex           // TODO do we need a lock here?
	metricsHandler metrics.IMetricsHandler // Global metricsHandler object

	waitGroup               *sync.WaitGroup
	clientHandlerContext    context.Context
	clientHandlerCancelFunc context.CancelFunc
}

func NewClientConnector(connection net.Conn,
	requestChannel chan *Frame,
	metricsHandler metrics.IMetricsHandler,
	waitGroup *sync.WaitGroup,
	clientHandlerContext context.Context,
	clientHandlerCancelFunc context.CancelFunc) *ClientConnector {
	return &ClientConnector{
		connection:              connection,
		requestChannel:          requestChannel,
		responseChannel:         make(chan []byte),
		lock:                    &sync.RWMutex{},
		metricsHandler:          metricsHandler,
		waitGroup:               waitGroup,
		clientHandlerContext:    clientHandlerContext,
		clientHandlerCancelFunc: clientHandlerCancelFunc,
	}
}

/**
 *	Starts two listening loops: one for receiving requests from the client, one for the responses that must be sent to the client
 */
func (cc *ClientConnector) run() {
	cc.listenForRequests()
	cc.listenForResponses()
}

func (cc *ClientConnector) listenForRequests() {

	log.Tracef("listenForRequests for client %s", cc.connection.RemoteAddr())

	var err error
	cc.waitGroup.Add(1)

	go func() {
		defer cc.waitGroup.Done()
		defer close(cc.requestChannel)
		for {
			var frame *Frame
			frameHeader := make([]byte, cassHdrLen)
			frame, err = readAndParseFrame(cc.connection, frameHeader, cc.clientHandlerContext)

			if err != nil {
				if err == ShutdownErr {
					return
				}

				if err == io.EOF {
					log.Infof("in listenForRequests: %s disconnected", cc.connection.RemoteAddr())
				} else {
					log.Errorf("in listenForRequests: error reading: %s", err)
				}

				cc.clientHandlerCancelFunc()
				break
			}

			if frame.Direction != 0 {
				log.Debugf("Unexpected frame direction %d", frame.Direction)
				log.Error(errors.New("unexpected direction: frame not from client to db - skipping frame"))
				continue
			}

			log.Tracef("sending frame on channel ")
			cc.requestChannel <- frame
			log.Tracef("frame sent")
		}
	}()
}

// listens on responseChannel, dequeues any responses and sends them to the client
func (cc *ClientConnector) listenForResponses() error {
	clientAddrStr := cc.connection.RemoteAddr().String()
	log.Tracef("listenForResponses for client %s", clientAddrStr)

	cc.waitGroup.Add(1)
	var err error
	go func() {
		cc.waitGroup.Done()
		for {
			log.Tracef("Waiting for next response to dispatch to client %s", clientAddrStr)

			response, ok := <-cc.responseChannel

			if !ok {
				log.Infof("shutting down response forwarder to %s", clientAddrStr)
				return
			}

			log.Tracef("Response with opcode %d (%v) received, dispatching to client %s", response[4], string(*&response), clientAddrStr)
			err = writeToConnection(cc.connection, response)
			log.Tracef("Response with opcode %d dispatched to client %s", response[4], clientAddrStr)
			if err != nil {
				log.Errorf("Error writing response to client connection: %s", err)
				break
			}

		}
	}()
	return err
}
